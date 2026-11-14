package app

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Optional admin event log. Records never contain IP addresses, passwords,
// tokens, typed usernames or setting values.
const (
	eventLogMaxRetentionHours = 24 // hard cap, regardless of stored value
	eventLogMinRetentionHours = 1
	eventLogMaxRows           = 50000
	eventLogQueueSize         = 1024
	eventLogStateTTL          = 2 * time.Second
)

// validateLogSetting checks the logging settings; owned is false for other keys.
func validateLogSetting(key, val string) (owned bool, err error) {
	switch key {
	case "logging_enabled":
		if _, ok := parseStrictBool(val); !ok {
			return true, errLogSetting(key + " must be true or false")
		}
		return true, nil
	case "log_retention_hours":
		if _, ok := parseStrictInt(val, eventLogMinRetentionHours, eventLogMaxRetentionHours); !ok {
			return true, errLogSetting("log_retention_hours must be an integer from 1 to 24")
		}
		return true, nil
	}
	return false, nil
}

type errLogSetting string

func (e errLogSetting) Error() string { return string(e) }

type logState struct {
	enabled   bool
	retention int
	at        time.Time
	gen       uint64
}

type eventRec struct {
	actorID           int64
	actor             string
	action, tgt, info string
	at                time.Time
	done              chan struct{} // flush marker when non-nil
}

// eventLogger is embedded in Server as a single field.
type eventLogger struct {
	once  sync.Once
	ch    chan eventRec
	state atomic.Pointer[logState]
}

// effectiveRetention fails closed: anything invalid is 1h, never above 24h.
func effectiveRetention(v string) int {
	n, ok := parseStrictInt(v, eventLogMinRetentionHours, eventLogMaxRetentionHours)
	if !ok {
		return eventLogMinRetentionHours
	}
	return n
}

// refreshLogState reads the settings. It uses the DB connection, so it must not
// be called while a transaction or rows are open on this goroutine.
func (s *Server) refreshLogState() *logState {
	gen := s.St.SettingsGen()
	enabled, _ := parseStrictBool(s.St.Setting("logging_enabled"))
	st := &logState{enabled: enabled, retention: effectiveRetention(s.St.Setting("log_retention_hours")), at: time.Now(), gen: gen}
	s.eventLog.state.Store(st)
	return st
}

func (s *Server) logConfig() *logState {
	st := s.eventLog.state.Load()
	if st == nil || st.gen != s.St.SettingsGen() || time.Since(st.at) > eventLogStateTTL {
		return s.refreshLogState()
	}
	return st
}

func clip(v string, n int) string {
	v = strings.ToValidUTF8(v, "")
	if utf8.RuneCountInString(v) <= n {
		return v
	}
	return string([]rune(v)[:n])
}

// logEvent records an event best-effort. It never touches the database from a
// request goroutine except for a cached settings refresh, so call it only when
// no transaction or rows are open on the single DB connection.
func (s *Server) logEvent(actorID int64, actorName, action, target, detail string) {
	if !s.logConfig().enabled {
		return
	}
	s.eventLog.once.Do(func() {
		s.eventLog.ch = make(chan eventRec, eventLogQueueSize)
		go s.eventWriter(s.eventLog.ch)
	})
	select {
	case s.eventLog.ch <- eventRec{actorID: actorID, actor: clip(actorName, 100), action: clip(action, 60), tgt: clip(target, 120), info: clip(detail, 200), at: time.Now().UTC()}:
	default: // bounded: drop on overflow
	}
}

func (s *Server) eventWriter(ch chan eventRec) {
	for ev := range ch {
		if ev.done != nil {
			close(ev.done)
			continue
		}
		// Cached flag only; the writer never blocks on settings refresh.
		if st := s.eventLog.state.Load(); st == nil || !st.enabled {
			continue
		}
		var actor any
		if ev.actorID > 0 {
			actor = ev.actorID
		}
		s.St.DB.Exec(`INSERT INTO event_log(created_at,actor_id,actor_name,action,target,detail) VALUES(?,?,?,?,?,?)`,
			ev.at, actor, ev.actor, ev.action, ev.tgt, ev.info)
	}
}

// flushEvents waits (bounded) until queued events are written.
func (s *Server) flushEvents() {
	ch := s.eventLog.ch
	if ch == nil {
		return
	}
	done := make(chan struct{})
	select {
	case ch <- eventRec{done: done}:
	case <-time.After(2 * time.Second):
		return
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// wipeEventLog removes every stored event (used when logging is switched off).
func (s *Server) wipeEventLog() {
	s.flushEvents()
	s.St.DB.Exec(`DELETE FROM event_log`)
}

// purgeEventLog applies retention and the row cap; called from maintain().
func (s *Server) purgeEventLog() {
	st := s.refreshLogState()
	if !st.enabled {
		s.St.DB.Exec(`DELETE FROM event_log`)
		return
	}
	cut := time.Now().UTC().Add(-time.Duration(st.retention) * time.Hour)
	s.St.DB.Exec(`DELETE FROM event_log WHERE created_at<?`, cut)
	s.St.DB.Exec(`DELETE FROM event_log WHERE id<=COALESCE((SELECT id FROM event_log ORDER BY id DESC LIMIT 1 OFFSET ?),0)`, eventLogMaxRows)
}

func likeEscape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `%`, `\%`)
	return strings.ReplaceAll(v, `_`, `\_`)
}

// listLogs: GET /api/logs (admin only).
func (s *Server) listLogs(w http.ResponseWriter, r *http.Request) {
	limit, offset := 50, 0
	for key, dest := range map[string]*int{"limit": &limit, "offset": &offset} {
		if value := r.URL.Query().Get(key); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || (key == "limit" && n == 0) {
				jerr(w, 400, "invalid pagination")
				return
			}
			*dest = n
		}
	}
	if limit > 100 {
		limit = 100
	}
	s.flushEvents()
	st := s.refreshLogState()
	out := []map[string]any{}
	var total int64
	if st.enabled {
		cut := time.Now().UTC().Add(-time.Duration(st.retention) * time.Hour)
		where, args := `created_at>=?`, []any{cut}
		if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
			p := "%" + likeEscape(q) + "%"
			where += ` AND (lower(action) LIKE ? ESCAPE '\' OR lower(actor_name) LIKE ? ESCAPE '\' OR lower(target) LIKE ? ESCAPE '\')`
			args = append(args, p, p, p)
		}
		if err := s.St.DB.QueryRow(`SELECT COUNT(*) FROM event_log WHERE `+where, args...).Scan(&total); err != nil {
			jerr(w, 500, "database error")
			return
		}
		rows, err := s.St.DB.Query(`SELECT id,created_at,actor_name,action,target,detail FROM event_log WHERE `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
		if err != nil {
			jerr(w, 500, "database error")
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var at time.Time
			var actor, action, target, detail string
			if err := rows.Scan(&id, &at, &actor, &action, &target, &detail); err != nil {
				jerr(w, 500, "database error")
				return
			}
			out = append(out, map[string]any{"id": id, "created_at": at, "actor": actor, "action": action, "target": target, "detail": detail})
		}
		if rows.Err() != nil {
			jerr(w, 500, "database error")
			return
		}
	}
	jout(w, 200, map[string]any{"logs": out, "total": total, "enabled": st.enabled, "retention_hours": st.retention})
}

// logShare records an authenticated user's action on a share.
func (s *Server) logShare(r *http.Request, action string, shareID int64, detail string) {
	u := s.ctxUser(r)
	if u == nil {
		return
	}
	s.logEvent(u.ID, u.Username, action, "share#"+strconv.FormatInt(shareID, 10), detail)
}

// userTarget labels a user as "user#ID (username)" for the audit log, so entries
// stay readable. It never queries while logging is switched off, and must not be
// called while rows or a transaction hold the single database connection.
func (s *Server) userTarget(id int64) string {
	label := "user#" + strconv.FormatInt(id, 10)
	if !s.logConfig().enabled {
		return label
	}
	var name string
	if err := s.St.DB.QueryRow(`SELECT username FROM users WHERE id=?`, id).Scan(&name); err == nil && name != "" {
		label += " (" + name + ")"
	}
	return label
}
