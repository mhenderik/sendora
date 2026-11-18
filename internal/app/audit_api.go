package app

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mhenderik/sendora/internal/store"
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
	if strings.ContainsRune("=+-@|%＝＋－＠", first) || trimmed != v {
		return "'" + v
	}
	return v
}

func likeEscape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `%`, `\%`)
	return strings.ReplaceAll(v, `_`, `\_`)
}

const (
	auditExportMax = 500000
	auditPageMax   = 100
)

// parseAuditTime accepts RFC 3339 or a plain date (UTC).
func parseAuditTime(v string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// auditFilter builds the WHERE clause of a log query. hideShare removes the
// share category (administrators never see it).
// searchTerms splits a search text into lower-case words; spaces, underscores, dashes
// and the wildcards * and % all separate words.
func searchTerms(v string) []string {
	return strings.FieldsFunc(strings.ToLower(v), func(r rune) bool {
		return unicode.IsSpace(r) || r == '_' || r == '-' || r == '*' || r == '%'
	})
}

func auditFilter(q url.Values, hideShare bool) (where string, args []any, msg string) {
	conds := []string{"1=1"}
	if hideShare {
		conds = append(conds, "category<>'share'")
	}
	// Free text: every word (split on spaces, underscores, dashes and * wildcards) must
	// occur somewhere in the action, actor, target, detail or category. So "create_user"
	// finds "user_created" and "create" finds all creations.
	for _, term := range searchTerms(q.Get("q")) {
		p := "%" + likeEscape(term) + "%"
		conds = append(conds, `(lower(action) LIKE ? ESCAPE '\' OR lower(actor_name) LIKE ? ESCAPE '\' OR lower(target) LIKE ? ESCAPE '\' OR lower(detail) LIKE ? ESCAPE '\' OR lower(category) LIKE ? ESCAPE '\')`)
		args = append(args, p, p, p, p, p)
	}
	if v := q.Get("category"); v != "" {
		if !auditCategories[v] {
			return "", nil, "invalid category"
		}
		conds = append(conds, "category=?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.Get("action")); v != "" {
		conds = append(conds, "action=?")
		args = append(args, v)
	}
	if v := strings.ToLower(strings.TrimSpace(q.Get("actor"))); v != "" {
		conds = append(conds, `lower(actor_name) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscape(v)+"%")
	}
	if v := q.Get("outcome"); v != "" {
		if !auditOutcomes[v] {
			return "", nil, "invalid outcome"
		}
		conds = append(conds, "outcome=?")
		args = append(args, v)
	}
	if v := q.Get("from"); v != "" {
		t, ok := parseAuditTime(v)
		if !ok {
			return "", nil, "invalid from time"
		}
		conds = append(conds, "ts>=?")
		args = append(args, t.Format(store.AuditTimeLayout))
	}
	if v := q.Get("to"); v != "" {
		t, ok := parseAuditTime(v)
		if !ok {
			return "", nil, "invalid to time"
		}
		if len(v) == len("2006-01-02") { // a date means the whole day
			t = t.AddDate(0, 0, 1)
		}
		conds = append(conds, "ts<=?")
		args = append(args, t.Format(store.AuditTimeLayout))
	}
	return strings.Join(conds, " AND "), args, ""
}

func parsePaging(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	limit = 50
	for key, dest := range map[string]*int{"limit": &limit, "offset": &offset} {
		if value := r.URL.Query().Get(key); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || (key == "limit" && n == 0) {
				jerr(w, 400, "invalid pagination")
				return 0, 0, false
			}
			*dest = n
		}
	}
	if limit > auditPageMax {
		limit = auditPageMax
	}
	return limit, offset, true
}

const auditCols = `seq,ts,category,actor_id,actor_name,actor_role,action,target,outcome,detail,ip,prev_hash,hash`

// auditRows reads matching records. The rows are buffered so the single pooled
// connection is free again before anything is written to the client.
func (s *Server) auditRows(where string, args []any, order string, limit, offset int) ([]store.AuditRecord, error) {
	q := `SELECT ` + auditCols + ` FROM audit_log WHERE ` + where + ` ORDER BY seq ` + order
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(append([]any{}, args...), limit, offset)
	}
	rows, err := s.St.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.AuditRecord
	for rows.Next() {
		var x store.AuditRecord
		if err := rows.Scan(&x.Seq, &x.TS, &x.Category, &x.ActorID, &x.ActorName, &x.ActorRole, &x.Action, &x.Target, &x.Outcome, &x.Detail, &x.IP, &x.PrevHash, &x.Hash); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func auditTime(ts string) time.Time {
	t, _ := time.Parse(store.AuditTimeLayout, ts)
	return t
}

// listLogs: GET /api/logs (administrators). Every category except share.
func (s *Server) listLogs(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parsePaging(w, r)
	if !ok {
		return
	}
	where, args, msg := auditFilter(r.URL.Query(), false)
	if msg != "" {
		jerr(w, 400, msg)
		return
	}
	var total int64
	if err := s.St.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE `+where, args...).Scan(&total); err != nil {
		jerr(w, 500, "database error")
		return
	}
	recs, err := s.auditRows(where, args, "DESC", limit, offset)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	out := make([]map[string]any, 0, len(recs))
	for _, x := range recs {
		out = append(out, map[string]any{"id": x.Seq, "created_at": auditTime(x.TS), "actor": x.ActorName, "role": x.ActorRole,
			"category": x.Category, "action": x.Action, "target": x.Target, "outcome": x.Outcome, "detail": x.Detail})
	}
	jout(w, 200, map[string]any{"logs": out, "total": total, "enabled": true, "retention_days": s.auditRetentionDays()})
}

// logsStatus: GET /api/logs/status (administrators): whether the chain verifies.
func (s *Server) logsStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.auditStatus()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	jout(w, 200, map[string]any{"verified": st.OK, "retention_days": s.auditRetentionDays(), "ip_logging": s.St.SettingBool("audit_log_ip")})
}

func writeCSV(w http.ResponseWriter, name string, records [][]string) {
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="`+name+`-`+time.Now().UTC().Format("20060102-150405")+`.csv"`)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	cw := csv.NewWriter(w)
	cw.WriteAll(records)
}

// exportDetail describes an export for the audit trail: format, number of records
// and the filters used, so it is clear which part of the log left the system.
func exportDetail(format string, n int, q url.Values) string {
	parts := []string{format, strconv.Itoa(n) + " records"}
	for _, k := range []string{"q", "category", "action", "actor", "outcome", "from", "to"} {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			parts = append(parts, k+"="+clip(v, 40))
		}
	}
	return strings.Join(parts, " ")
}

// exportLogs: GET /api/logs/export (administrators), CSV without share events.
func (s *Server) exportLogs(w http.ResponseWriter, r *http.Request) {
	where, args, msg := auditFilter(r.URL.Query(), false)
	if msg != "" {
		jerr(w, 400, msg)
		return
	}
	recs, err := s.auditRows(where, args, "ASC", auditExportMax+1, 0)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	if len(recs) > auditExportMax {
		jerr(w, 413, "too many records; narrow the filter")
		return
	}
	records := [][]string{{"id", "time_utc", "category", "user", "role", "action", "target", "outcome", "details"}}
	for _, x := range recs {
		records = append(records, []string{strconv.FormatInt(x.Seq, 10), x.TS, x.Category, csvSafe(x.ActorName), x.ActorRole,
			csvSafe(x.Action), csvSafe(x.Target), x.Outcome, csvSafe(x.Detail)})
	}
	if u := s.ctxUser(r); u != nil {
		s.logEvent(u.ID, u.Username, "audit_exported", "audit_log", exportDetail("csv", len(recs), r.URL.Query()))
	}
	writeCSV(w, "sendora-logs", records)
}

// ---------- auditor endpoints ----------

func auditJSON(x store.AuditRecord) map[string]any {
	return map[string]any{"seq": x.Seq, "ts": x.TS, "category": x.Category, "actor_id": x.ActorID, "actor": x.ActorName, "actor_role": x.ActorRole,
		"action": x.Action, "target": x.Target, "outcome": x.Outcome, "detail": x.Detail, "ip": x.IP, "prev_hash": x.PrevHash, "hash": x.Hash}
}

// auditList: GET /api/audit (auditors): every category.
func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parsePaging(w, r)
	if !ok {
		return
	}
	where, args, msg := auditFilter(r.URL.Query(), false)
	if msg != "" {
		jerr(w, 400, msg)
		return
	}
	var total int64
	if err := s.St.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE `+where, args...).Scan(&total); err != nil {
		jerr(w, 500, "database error")
		return
	}
	recs, err := s.auditRows(where, args, "DESC", limit, offset)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	out := make([]map[string]any, 0, len(recs))
	for _, x := range recs {
		out = append(out, auditJSON(x))
	}
	if u := s.ctxUser(r); u != nil {
		s.auditViewedOnce(u)
	}
	jout(w, 200, map[string]any{"records": out, "total": total, "retention_days": s.auditRetentionDays()})
}

// auditExport: GET /api/audit/export?format=csv|jsonl (auditors).
func (s *Server) auditExport(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "jsonl" {
		jerr(w, 400, "format must be csv or jsonl")
		return
	}
	where, args, msg := auditFilter(r.URL.Query(), false)
	if msg != "" {
		jerr(w, 400, msg)
		return
	}
	recs, err := s.auditRows(where, args, "ASC", auditExportMax+1, 0)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	if len(recs) > auditExportMax {
		jerr(w, 413, "too many records; narrow the filter")
		return
	}
	if u := s.ctxUser(r); u != nil {
		s.logEvent(u.ID, u.Username, "audit_exported", "audit_log", exportDetail(format, len(recs), r.URL.Query()))
	}
	if format == "csv" {
		records := [][]string{{"seq", "time_utc", "category", "actor_id", "user", "role", "action", "target", "outcome", "details", "ip", "prev_hash", "hash"}}
		for _, x := range recs {
			records = append(records, []string{strconv.FormatInt(x.Seq, 10), x.TS, x.Category, strconv.FormatInt(x.ActorID, 10), csvSafe(x.ActorName), x.ActorRole,
				csvSafe(x.Action), csvSafe(x.Target), x.Outcome, csvSafe(x.Detail), x.IP, x.PrevHash, x.Hash})
		}
		writeCSV(w, "sendora-audit", records)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/x-ndjson; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="sendora-audit-`+time.Now().UTC().Format("20060102-150405")+`.jsonl"`)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	enc := json.NewEncoder(w)
	for _, x := range recs {
		enc.Encode(x)
	}
}

// auditStatusHandler: GET /api/audit/status (auditors).
func (s *Server) auditStatusHandler(w http.ResponseWriter, r *http.Request) {
	st, err := s.auditStatus()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	out := map[string]any{"verified": st.OK, "count": st.Count, "oldest_ts": st.OldestTS, "last_seq": st.LastSeq,
		"retention_days": s.auditRetentionDays(), "ip_logging": s.St.SettingBool("audit_log_ip"),
		"checkpoint":     map[string]any{"seq": st.CheckpointSeq, "hash": st.CheckpointHash},
		"write_failures": s.audit.fails.Load()}
	if !st.OK {
		out["first_broken_seq"] = st.FirstBroken
		out["reason"] = st.Reason
	}
	jout(w, 200, out)
}
