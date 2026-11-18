package app

import (
	"context"
	"log"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mhenderik/sendora/internal/store"
)

// Tamper-evident audit log (see internal/store/audit.go for the chain). Logging
// is always on. Records never contain passwords, tokens, secrets, link IDs, file
// contents or the values of secret settings; the client IP is stored only while
// the audit_log_ip setting is on.

const (
	auditViewedEvery = 5 * time.Minute
	auditStatusTTL   = 5 * time.Second
)

// Categories and outcomes of audit records.
const (
	catAuth     = "auth"
	catAccount  = "account"
	catAdmin    = "admin"
	catSecurity = "security"
	catShare    = "share"
	catSystem   = "system"
)

var auditCategories = map[string]bool{catAuth: true, catAccount: true, catAdmin: true, catSecurity: true, catShare: true, catSystem: true}
var auditOutcomes = map[string]bool{"ok": true, "denied": true, "failed": true, "attempted": true}

// auditEntry is a record before it is chained.
type auditEntry struct {
	Category, Action, Target, Outcome, Detail string
	ActorID                                   int64
	ActorName, ActorRole                      string
}

// auditReq is the request-scoped state shared by the audit middleware, the
// authentication middleware and logEvent.
type auditReq struct {
	r        *http.Request
	user     atomic.Pointer[User]
	wrote    atomic.Bool // any record was written for this request
	logged   atomic.Bool // the handler itself recorded an outcome
	critical atomic.Pointer[auditEntry]
}

type auditReqKeyType struct{}

var auditReqKey auditReqKeyType

func auditReqFrom(ctx context.Context) *auditReq {
	st, _ := ctx.Value(auditReqKey).(*auditReq)
	return st
}

type auditState struct {
	fails    atomic.Int64
	appended atomic.Int64
	reqs     sync.Map // goroutine id -> *auditReq
	hook     atomic.Pointer[func() error]

	viewMu sync.Mutex
	viewed map[int64]time.Time

	statusMu  sync.Mutex
	statusVal store.AuditStatus
	statusAt  time.Time
	statusGen int64
}

// goroutineID identifies the calling goroutine. logEvent has no request
// parameter (and its call sites must not change), so the request a record
// belongs to is found through the goroutine that serves it.
func goroutineID() uint64 {
	var b [48]byte
	n := runtime.Stack(b[:], false)
	s := strings.TrimPrefix(string(b[:n]), "goroutine ")
	if i := strings.IndexByte(s, ' '); i > 0 {
		id, _ := strconv.ParseUint(s[:i], 10, 64)
		return id
	}
	return 0
}

func (s *Server) currentAuditReq() *auditReq {
	if v, ok := s.audit.reqs.Load(goroutineID()); ok {
		return v.(*auditReq)
	}
	return nil
}

func roleOfUser(u *User) string {
	switch {
	case u == nil:
		return "anonymous"
	case u.Admin:
		return "admin"
	case u.Auditor:
		return "auditor"
	case u.IsGuest:
		return "guest"
	}
	return "user"
}

// classifyAction maps an action name to its category and outcome.
func classifyAction(action string) (category, outcome string) {
	outcome = "ok"
	switch {
	case strings.HasSuffix(action, "_failed"):
		outcome = "failed"
	case action == "login_lockout", action == "access_denied", strings.HasSuffix(action, "_denied"):
		outcome = "denied"
	}
	switch action {
	case "login_ok", "login_failed", "login_lockout", "logout", "mfa_login_ok", "mfa_failed", "mfa_enrolled", "mfa_removed",
		"session_reuse_detected", "password_changed", "password_reset":
		return catAuth, outcome
	case "ip_banned", "ip_unbanned", "access_denied", "file_decrypt_failed", "key_fingerprint_changed", "audit_viewed", "audit_exported":
		return catSecurity, outcome
	case "system_start", "system_stop", "audit_purged", "audit_purge_failed":
		return catSystem, outcome
	case "mfa_reset":
		return catAccount, outcome
	}
	switch {
	case strings.HasPrefix(action, "share"), strings.HasPrefix(action, "download_"), strings.HasPrefix(action, "invite_"):
		return catShare, outcome
	case strings.HasPrefix(action, "user_"):
		return catAccount, outcome
	}
	return catAdmin, outcome
}

// routeCategory is the category of a generic api_mutation record.
func routeCategory(pattern string) string {
	switch {
	case strings.HasPrefix(pattern, "/api/shares"), strings.HasPrefix(pattern, "/api/uploads"), strings.HasPrefix(pattern, "/api/reverse-shares"):
		return catShare
	case strings.HasPrefix(pattern, "/api/auth"):
		return catAuth
	case strings.HasPrefix(pattern, "/api/users"):
		return catAccount
	case pattern == "/api/setup":
		return catSystem
	}
	return catAdmin
}

// auditRole is the role label of an actor.
func (s *Server) auditRole(id int64, name string, st *auditReq) string {
	if st != nil {
		if u := st.user.Load(); u != nil && u.ID == id {
			return roleOfUser(u)
		}
	}
	if id <= 0 {
		if name == "system" {
			return "system"
		}
		return "anonymous"
	}
	var admin, auditor, guest bool
	if s.St.DB.QueryRow(`SELECT admin,is_auditor,is_guest FROM users WHERE id=?`, id).Scan(&admin, &auditor, &guest) != nil {
		return "user"
	}
	return roleOfUser(&User{Admin: admin, Auditor: auditor, IsGuest: guest})
}

// auditWrite chains and stores one record synchronously. A failed write is
// retried once; after that the failure is reported on stderr and counted.
func (s *Server) auditWrite(e auditEntry, st *auditReq) error {
	if !auditCategories[e.Category] {
		e.Category = catAdmin
	}
	if !auditOutcomes[e.Outcome] {
		e.Outcome = "ok"
	}
	var ip string
	if st != nil && st.r != nil && s.St.SettingBool("audit_log_ip") {
		ip = s.clientIP(st.r)
	}
	rec := store.AuditRecord{Category: e.Category, ActorID: e.ActorID, ActorName: clip(e.ActorName, 100), ActorRole: e.ActorRole,
		Action: clip(e.Action, 80), Target: clip(e.Target, 160), Outcome: e.Outcome, Detail: e.Detail, IP: ip}
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if h := s.audit.hook.Load(); h != nil {
			if err = (*h)(); err != nil {
				continue
			}
		}
		if _, err = s.St.AuditAppend(rec); err == nil {
			s.audit.appended.Add(1)
			if st != nil {
				st.wrote.Store(true)
			}
			return nil
		}
	}
	s.audit.fails.Add(1)
	log.Printf("AUDIT WRITE FAILED (action %s): %v", rec.Action, err)
	return err
}

// logEvent records an audit event synchronously. Call it only when no
// transaction or rows are open on the single database connection. A failure is
// reported on stderr and counted, never raised.
func (s *Server) logEvent(actorID int64, actorName, action, target, detail string) {
	st := s.currentAuditReq()
	cat, outcome := classifyAction(action)
	e := auditEntry{Category: cat, Action: action, Target: target, Outcome: outcome, Detail: detail,
		ActorID: actorID, ActorName: actorName, ActorRole: s.auditRole(actorID, actorName, st)}
	if s.auditWrite(e, st) == nil && st != nil {
		st.logged.Store(true)
	}
}

// logShare records an authenticated user's action on a share.
func (s *Server) logShare(r *http.Request, action string, shareID int64, detail string) {
	u := s.ctxUser(r)
	if u == nil {
		return
	}
	s.logEvent(u.ID, u.Username, action, "share#"+strconv.FormatInt(shareID, 10), detail)
}

// userTarget labels a user as "user#ID (username)" for the audit log. It must
// not be called while rows or a transaction hold the single database connection.
func (s *Server) userTarget(id int64) string {
	label := "user#" + strconv.FormatInt(id, 10)
	var name string
	if err := s.St.DB.QueryRow(`SELECT username FROM users WHERE id=?`, id).Scan(&name); err == nil && name != "" {
		label += " (" + name + ")"
	}
	return label
}

// flushEvents is kept for callers of the former queue; writes are synchronous.
func (s *Server) flushEvents() {}

// auditCritical writes an outcome=attempted record BEFORE a critical action
// runs. When the record cannot be stored it answers 503 and returns false, so
// no critical administrative action happens unlogged.
func (s *Server) auditCritical(w http.ResponseWriter, r *http.Request, action, target, detail string) bool {
	u := s.ctxUser(r)
	st := auditReqFrom(r.Context())
	cat, _ := classifyAction(action)
	e := auditEntry{Category: cat, Action: action, Target: target, Outcome: "attempted", Detail: detail, ActorRole: roleOfUser(u)}
	if u != nil {
		e.ActorID, e.ActorName = u.ID, u.Username
	}
	if err := s.auditWrite(e, st); err != nil {
		jerr(w, http.StatusServiceUnavailable, "the audit log is unavailable; the action was not performed")
		return false
	}
	if st != nil {
		e.Outcome = "ok"
		st.critical.Store(&e)
	}
	return true
}

// critical is the route middleware that audits an action before it runs. noun
// labels the target ("user" gives "user#<id>" when the route has an {id}).
func (s *Server) critical(action, noun string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target := noun
			for _, p := range []string{"id", "name"} {
				if v := chi.URLParam(r, p); v != "" {
					target = noun + "#" + clip(v, 60)
					break
				}
			}
			if !s.auditCritical(w, r, action, target, "") {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// auditExempt lists mutating routes the generic middleware deliberately does not
// record, each with its reason. TestAuditCoversEveryMutatingRoute keeps it exact.
var auditExempt = map[string]string{
	"POST /api/auth/refresh":                "silent token rotation that every signed-in browser repeats; session reuse and sign-in events are recorded explicitly",
	"PATCH /api/uploads/{id}/files/{idx}":   "one request per uploaded chunk; the upload start and the finished share are recorded",
	"POST /api/auth/mfa/webauthn/begin":     "challenge request without effect; the matching finish request and failures are recorded",
	"POST /api/users/me/mfa/webauthn/begin": "challenge request without effect; the matching finish request is recorded",
	"POST /api/users/me/mfa/totp/begin":     "secret preparation without effect; the confirm request is recorded",
	"POST /api/email-templates/preview":     "renders a preview without storing anything",
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// auditMiddleware gives every request an audit state and, after the handler ran,
// records what the handler did not: denied access on authenticated routes and a
// generic api_mutation entry for mutating requests.
func (s *Server) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &auditReq{r: r}
		r = r.WithContext(context.WithValue(r.Context(), auditReqKey, st))
		gid := goroutineID()
		s.audit.reqs.Store(gid, st)
		defer s.audit.reqs.Delete(gid)
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		code := ww.Status()
		if code == 0 {
			code = http.StatusOK
		}
		pattern := ""
		if rc := chi.RouteContext(r.Context()); rc != nil {
			pattern = rc.RoutePattern()
		}
		key := r.Method + " " + pattern
		u := st.user.Load()
		e := auditEntry{ActorRole: roleOfUser(u)}
		if u != nil {
			e.ActorID, e.ActorName = u.ID, u.Username
		}
		switch {
		case st.critical.Load() != nil && !st.logged.Load():
			c := st.critical.Load()
			e.Category, e.Action, e.Target = c.Category, c.Action, c.Target
			e.Outcome = outcomeOfStatus(code)
			e.Detail = "status=" + strconv.Itoa(code)
		case (code == http.StatusUnauthorized || code == http.StatusForbidden) && (u != nil || r.Header.Get("Authorization") != "") && !st.logged.Load():
			e.Category, e.Action, e.Target, e.Outcome = catSecurity, "access_denied", pattern, "denied"
			e.Detail = r.Method + " status=" + strconv.Itoa(code)
		case isMutating(r.Method) && !st.wrote.Load() && auditExempt[key] == "":
			e.Category, e.Action, e.Outcome = routeCategory(pattern), "api_mutation", outcomeOfStatus(code)
			e.Action = key
			e.Detail = "status=" + strconv.Itoa(code)
		default:
			return
		}
		_ = s.auditWrite(e, st)
	})
}

func outcomeOfStatus(code int) string {
	switch {
	case code < 400:
		return "ok"
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return "denied"
	}
	return "failed"
}

// auditAccessDenied records a denied access (for example to a share that belongs
// to somebody else) for the request being served.
func (s *Server) auditAccessDenied(r *http.Request, category, target, detail string) {
	u := s.ctxUser(r)
	st := auditReqFrom(r.Context())
	e := auditEntry{Category: category, Action: "access_denied", Target: target, Outcome: "denied", Detail: detail, ActorRole: roleOfUser(u)}
	if u != nil {
		e.ActorID, e.ActorName = u.ID, u.Username
	}
	if s.auditWrite(e, st) == nil && st != nil {
		st.logged.Store(true)
	}
}

// auditSetActor tells the audit state who the authenticated user of this request is.
func (s *Server) auditSetActor(r *http.Request, u *User) {
	if st := auditReqFrom(r.Context()); st != nil {
		st.user.Store(u)
	}
}

// auditDecryptFailed records that a stored file could not be decrypted (row id only).
func (s *Server) auditDecryptFailed(fileID int64) {
	s.logEvent(0, "system", "file_decrypt_failed", "file#"+strconv.FormatInt(fileID, 10), "")
}

// auditRetentionDays is the effective retention (1..365; an invalid stored value
// falls back to the default instead of shortening the log).
func (s *Server) auditRetentionDays() int {
	n, ok := parseStrictInt(s.St.Setting("audit_retention_days"), 1, 365)
	if !ok {
		return 90
	}
	return n
}

// purgeAudit applies the retention; called from maintain().
func (s *Server) purgeAudit() {
	n, err := s.St.AuditPurge(time.Now().UTC().AddDate(0, 0, -s.auditRetentionDays()))
	if err != nil {
		log.Printf("audit retention purge: %v", err)
		s.logEvent(0, "system", "audit_purge_failed", "", "retention purge refused or failed")
		return
	}
	if n > 0 {
		s.logEvent(0, "system", "audit_purged", "", strconv.FormatInt(n, 10)+" records past retention")
	}
}

// auditStartup records the start of the process and checks the database key.
func (s *Server) auditStartup() {
	fp := s.St.KeyFingerprint()
	if old := s.St.Setting("audit_key_fingerprint"); old != "" && old != fp {
		s.logEvent(0, "system", "key_fingerprint_changed", "", "was "+clip(old, 20)+" now "+fp)
	}
	if old := s.St.Setting("audit_key_fingerprint"); old != fp {
		s.St.DB.Exec(`INSERT INTO settings(key,value,grp) VALUES('audit_key_fingerprint',?, 'secret') ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fp)
	}
	s.logEvent(0, "system", "system_start", "", "version="+clip(s.Version, 40)+" key="+fp)
}

// auditShutdown records an orderly stop.
func (s *Server) auditShutdown() { s.logEvent(0, "system", "system_stop", "", "") }

// auditStatus verifies the chain, reusing a result for a few seconds while no
// record was added.
func (s *Server) auditStatus() (store.AuditStatus, error) {
	a := &s.audit
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	if !a.statusAt.IsZero() && time.Since(a.statusAt) < auditStatusTTL && a.statusGen == a.appended.Load() {
		return a.statusVal, nil
	}
	st, err := s.St.AuditVerify()
	if err != nil {
		return st, err
	}
	a.statusVal, a.statusAt, a.statusGen = st, time.Now(), a.appended.Load()
	return st, nil
}

// auditViewedOnce records audit_viewed at most once per period per actor.
func (s *Server) auditViewedOnce(u *User) {
	a := &s.audit
	a.viewMu.Lock()
	if a.viewed == nil {
		a.viewed = map[int64]time.Time{}
	}
	last, ok := a.viewed[u.ID]
	if ok && time.Since(last) < auditViewedEvery {
		a.viewMu.Unlock()
		return
	}
	a.viewed[u.ID] = time.Now()
	a.viewMu.Unlock()
	s.logEvent(u.ID, u.Username, "audit_viewed", "audit_log", "")
}

// auditorAllowed is the allowlist of an auditor session: its own profile and
// password, and the audit endpoints. Everything else is refused.
func auditorAllowed(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/users/me":
		return r.Method == http.MethodGet || r.Method == http.MethodPut
	case "/api/audit", "/api/audit/export", "/api/audit/status":
		return r.Method == http.MethodGet
	}
	return false
}

func (s *Server) auditor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := s.ctxUser(r); u == nil || !(u.Auditor || u.Admin) {
			jerr(w, 403, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clip(v string, n int) string {
	v = strings.ToValidUTF8(v, "")
	if utf8.RuneCountInString(v) <= n {
		return v
	}
	return string([]rune(v)[:n])
}
