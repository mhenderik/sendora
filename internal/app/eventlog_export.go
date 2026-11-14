package app

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// csvSafe neutralises spreadsheet formula injection in exported cells. Some
// spreadsheet programs skip leading whitespace or control characters and accept
// full-width operators, so those are looked past before the first character is
// checked.
func csvSafe(v string) string {
	trimmed := strings.TrimLeftFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	if trimmed == "" {
		return v
	}
	first, _ := utf8.DecodeRuneInString(trimmed)
	if strings.ContainsRune("=+-@|%\uFF1D\uFF0B\uFF0D\uFF20", first) || trimmed != v {
		return "'" + v
	}
	return v
}

// exportLogs: GET /api/logs/export (admin only). Streams the entries inside the
// retention window (optionally filtered by q) as CSV, oldest first.
func (s *Server) exportLogs(w http.ResponseWriter, r *http.Request) {
	s.flushEvents()
	st := s.refreshLogState()
	if !st.enabled {
		jerr(w, 409, "logging is turned off")
		return
	}
	cut := time.Now().UTC().Add(-time.Duration(st.retention) * time.Hour)
	where, args := `created_at>=?`, []any{cut}
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
		p := "%" + likeEscape(q) + "%"
		where += ` AND (lower(action) LIKE ? ESCAPE '\' OR lower(actor_name) LIKE ? ESCAPE '\' OR lower(target) LIKE ? ESCAPE '\')`
		args = append(args, p, p, p)
	}
	rows, err := s.St.DB.Query(`SELECT id,created_at,actor_name,action,target,detail FROM event_log WHERE `+where+` ORDER BY id ASC LIMIT 50000`, args...)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	// Buffer first so a database error cannot produce a truncated download and
	// the single pooled connection is released before any further DB use.
	records := [][]string{{"id", "time_utc", "user", "action", "target", "details"}}
	for rows.Next() {
		var id int64
		var at time.Time
		var actor, action, target, detail string
		if err := rows.Scan(&id, &at, &actor, &action, &target, &detail); err != nil {
			rows.Close()
			jerr(w, 500, "database error")
			return
		}
		records = append(records, []string{strconv.FormatInt(id, 10), at.UTC().Format(time.RFC3339), csvSafe(actor), csvSafe(action), csvSafe(target), csvSafe(detail)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	if u := s.ctxUser(r); u != nil {
		s.logEvent(u.ID, u.Username, "logs_exported", "event_log", strconv.Itoa(len(records)-1)+" entries")
	}
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="sendora-logs-`+time.Now().UTC().Format("20060102-150405")+`.csv"`)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	cw := csv.NewWriter(w)
	cw.WriteAll(records)
}
