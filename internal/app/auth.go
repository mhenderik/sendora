package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/mhenderik/sendora/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const msgAdminAuditorExclusive = "an account cannot be both administrator and auditor"

type User struct {
	ID                   int64  `json:"id"`
	Username             string `json:"username"`
	Name                 string `json:"name"`
	Email                string `json:"email"`
	Admin                bool   `json:"admin"`
	MustChangePassword   bool   `json:"must_change_password"`
	IsGuest              bool   `json:"is_guest"`
	Auditor              bool   `json:"auditor"`
	EmailVerified        bool   `json:"email_verified"`
	CreatedAt            string `json:"created_at"`
	Language             string `json:"language"`
	UILanguage           string `json:"ui_language"`
	QuotaBytes           int64  `json:"quota_bytes"`
	QuotaInheritsDefault bool   `json:"quota_inherits_default"`
	MFAPolicy            string `json:"mfa_policy"`
	MFAEnabled           bool   `json:"mfa_enabled"`
	MFARequired          bool   `json:"mfa_required"`
	MFAEnrollRequired    bool   `json:"mfa_enroll_required"`
	mfaEpoch             int64  `json:"-"`
	loginFailures        int64  `json:"-"`
	loginLockedUntil     int64  `json:"-"`
	passwordHash         string
}

const userCols = `id,username,name,email,admin,must_change_password,is_guest,is_auditor,email_verified,created_at,password_hash,language,ui_language,quota_bytes,login_failures,login_locked_until,quota_inherits_default,(SELECT value FROM settings WHERE key='default_storage_quota_bytes'),mfa_policy,mfa_epoch`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	var quota, inheritance, defaultQuota sql.NullString
	err := row.Scan(&u.ID, &u.Username, &u.Name, &u.Email, &u.Admin, &u.MustChangePassword, &u.IsGuest, &u.Auditor, &u.EmailVerified, &u.CreatedAt, &u.passwordHash, &u.Language, &u.UILanguage, &quota, &u.loginFailures, &u.loginLockedUntil, &inheritance, &defaultQuota, &u.MFAPolicy, &u.mfaEpoch)
	if err != nil {
		return nil, err
	}
	u.QuotaBytes, u.QuotaInheritsDefault, err = store.ResolveQuota(quota, inheritance, defaultQuota)
	if err != nil {
		return nil, err
	}
	if !store.ValidMailCode(u.Language) {
		u.Language = "en"
	}
	if !store.ValidUICodeSyntax(u.UILanguage) {
		u.UILanguage = "en"
	}
	return u, err
}

func (s *Server) userByID(id int64) (*User, error) {
	return scanUser(s.St.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (s *Server) accessToken(u *User) string { return s.accessTokenAt(u, time.Now()) }

// accessTokenAt records when the session was signed in (auth_time), so actions
// that need a recent sign-in can check it; refresh carries the original time.
func (s *Server) accessTokenAt(u *User, authTime time.Time) string {
	claims := jwt.MapClaims{
		"sub":        strconv.FormatInt(u.ID, 10),
		"purpose":    "access",
		"jti":        randHex(16),
		"credential": s.credentialVersion(u),
		"auth_time":  authTime.Unix(),
		"exp":        time.Now().Add(15 * time.Minute).Unix(),
	}
	// Required but not yet enrolled: the token only reaches the enrolment routes.
	// It is decided when a token is issued (login/refresh), so a stricter policy
	// takes effect at the next login or refresh, not in the middle of a session.
	if s.mfaEnrollRequired(u) {
		claims["mfa_enroll"] = true
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	str, _ := t.SignedString(s.key(keyAccess))
	return str
}

// sessionMaxAge is the absolute lifetime of a session: refreshing never extends
// it beyond this time after the original sign-in.
const sessionMaxAge = 30 * 24 * time.Hour

// requestAuthTime returns the sign-in time recorded in the request's (already
// validated) access token, or the zero time.
func (s *Server) requestAuthTime(r *http.Request) time.Time {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(tok, claims, func(*jwt.Token) (any, error) { return s.key(keyAccess), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired()); err != nil {
		return time.Time{}
	}
	if v, ok := claims["auth_time"].(float64); ok && v > 0 {
		return time.Unix(int64(v), 0)
	}
	return time.Time{}
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, u *User) bool {
	return s.issueSessionSince(w, r, u, time.Now())
}

// sessionStartOf is the sign-in time of the current request's session, used when
// a profile or security change re-issues the session: it must not restart the
// 30-day limit or the recent-sign-in window.
func (s *Server) sessionStartOf(r *http.Request) time.Time {
	if t := s.requestAuthTime(r); !t.IsZero() && time.Since(t) < sessionMaxAge {
		return t
	}
	return time.Now()
}

func (s *Server) issueSessionSince(w http.ResponseWriter, r *http.Request, u *User, start time.Time) bool {
	rt := randHex(32)
	now := time.Now()
	exp := start.Add(sessionMaxAge)
	res, err := s.St.DB.Exec(`INSERT INTO refresh_tokens(hash,user_id,expires_at,issued_at,session_started) SELECT ?,id,?,?,? FROM users WHERE id=? AND password_hash=? AND email=? AND must_change_password=?`, hashToken(rt), exp, now, start, u.ID, u.passwordHash, u.Email, u.MustChangePassword)
	if err != nil {
		return false
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false
	}
	http.SetCookie(w, &http.Cookie{Name: "refresh", Value: rt, Path: "/api/auth", Expires: exp, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.secureCookies(r)})
	return true
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		tok := strings.TrimPrefix(h, "Bearer ")
		if tok == "" || tok == h {
			jerr(w, 401, "unauthorized")
			return
		}
		claims := jwt.MapClaims{}
		_, err := jwt.ParseWithClaims(tok, claims, func(t *jwt.Token) (any, error) { return s.key(keyAccess), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
		if err != nil {
			jerr(w, 401, "unauthorized")
			return
		}
		var revoked int
		if s.St.DB.QueryRow(`SELECT COUNT(*) FROM revoked_access_tokens WHERE hash=? AND expires_at>?`, hashToken(tok), time.Now()).Scan(&revoked) != nil || revoked != 0 {
			jerr(w, 401, "unauthorized")
			return
		}
		id, _ := strconv.ParseInt(fmt.Sprint(claims["sub"]), 10, 64)
		u, err := s.userByID(id)
		if err != nil || claims["purpose"] != "access" || claims["credential"] != s.credentialVersion(u) {
			jerr(w, 401, "unauthorized")
			return
		}
		if u.IsGuest {
			var valid int
			if s.St.DB.QueryRow(`SELECT COUNT(*) FROM reverse_invites WHERE guest_user_id=? AND completed_at IS NULL AND expires_at>?`, u.ID, time.Now()).Scan(&valid) != nil || valid != 1 {
				jerr(w, 403, "invite no longer valid")
				return
			}
			if !(r.Method == http.MethodGet && r.URL.Path == "/api/users/me") && r.URL.Path != "/api/uploads" && !strings.HasPrefix(r.URL.Path, "/api/uploads/") {
				jerr(w, 403, "guest session is upload-only")
				return
			}
		}
		s.auditSetActor(r, u)
		if u.Auditor && !auditorAllowed(r) {
			jerr(w, 403, "auditor accounts are read-only")
			return
		}
		if u.MustChangePassword && !(r.URL.Path == "/api/users/me" && (r.Method == http.MethodGet || r.Method == http.MethodPut)) {
			jerr(w, 403, "password change required")
			return
		}
		if enroll, _ := claims["mfa_enroll"].(bool); enroll && !mfaEnrollAllowed(r) {
			jerr(w, 403, "mfa enrollment required")
			return
		}
		next.ServeHTTP(w, withUser(r, u))
	})
}

func (s *Server) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := s.ctxUser(r); u == nil || !u.Admin {
			jerr(w, 403, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sessionResponse(w http.ResponseWriter, r *http.Request, u *User) {
	if u == nil {
		jerr(w, 500, "user policy unavailable")
		return
	}
	if !s.issueSession(w, r, u) {
		jerr(w, 401, "session changed; log in again")
		return
	}
	s.fillMFA(u)
	jout(w, 200, map[string]any{"access_token": s.accessToken(u), "user": u})
}

// ---------- setup / login ----------

type credentials struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	Language   string `json:"language"`
	UILanguage string `json:"ui_language"`
	SetupCode  string `json:"setup_code"`
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	// All addresses share this limit: many sources cannot add up to a code search.
	if wait, blocked := s.setupThrottle.blocked("setup"); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		jerr(w, 429, "too many failed setup attempts; try again later")
		return
	}
	var n int
	s.St.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n > 0 {
		jerr(w, 403, "already set up")
		return
	}
	var c credentials
	if decode(r, &c) != nil || c.Username == "" {
		jerr(w, 400, "username and password required")
		return
	}
	if !validUsername(c.Username) {
		jerr(w, 400, msgInvalidUsername)
		return
	}
	given := strings.ToUpper(strings.TrimSpace(c.SetupCode))
	if s.setupCode == "" || subtle.ConstantTimeCompare([]byte(given), []byte(s.setupCode)) != 1 {
		s.setupThrottle.fail("setup", setupFailLimit, setupFailWindow, setupFailBlock)
		markAuthFailure(w)
		jerr(w, 403, "invalid setup code: use the code printed in the server log")
		return
	}
	if !s.requireAccountPassword(w, c.Password) {
		return
	}
	email, err := canonicalEmail(c.Email)
	if err != nil {
		jerr(w, 400, "invalid email")
		return
	}
	c.Email = email
	if c.Language == "" {
		c.Language = "en"
	}
	if !s.St.ValidMailLanguage(c.Language) {
		jerr(w, 400, "unsupported language")
		return
	}
	if c.UILanguage == "" {
		c.UILanguage = "en"
	}
	if !s.St.ValidUILanguage(c.UILanguage) {
		jerr(w, 400, "unsupported UI language")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcryptCost)
	if err != nil {
		jerr(w, 400, "invalid password (8-72 bytes required)")
		return
	}
	res, err := s.St.DB.Exec(`INSERT INTO users(username,name,email,password_hash,admin,language,ui_language)
		SELECT ?,?,?,?,1,?,? WHERE NOT EXISTS (SELECT 1 FROM users)`, c.Username, c.Name, c.Email, string(hash), c.Language, c.UILanguage)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		jerr(w, 403, "already set up")
		return
	}
	id, _ := res.LastInsertId()
	u, _ := s.userByID(id)
	s.sessionResponse(w, r, u)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalLogin(w) {
		return
	}
	var c credentials
	if decode(r, &c) != nil {
		jerr(w, 400, "bad request")
		return
	}
	u, err := scanUser(s.St.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE username=? COLLATE NOCASE AND is_guest=0 ORDER BY username=? DESC LIMIT 1`, c.Username, c.Username))
	if err != nil {
		email, emailErr := canonicalEmail(c.Username)
		if emailErr == nil && email != "" {
			u, err = scanUser(s.St.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE lower(trim(email))=? AND is_guest=0 AND (SELECT COUNT(*) FROM users WHERE lower(trim(email))=?)=1`, email, email))
		}
	}
	if err != nil || u.passwordHash == "" {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(c.Password)) // equal timing
		// Never record the typed username: it may be a mistyped password.
		if err == nil {
			s.logEvent(u.ID, u.Username, "login_failed", "", "")
		} else {
			s.logEvent(0, "unknown", "login_failed", "", "")
		}
		rejectLogin(w)
		return
	}
	unlock := onceUnlock(lockStripe(&s.loginLocks, strconv.FormatInt(u.ID, 10)))
	defer unlock()
	// Reload after acquiring the account lock; aliases and peers share one counter.
	u, err = s.userByID(u.ID)
	if err != nil || u.IsGuest || u.passwordHash == "" || u.loginLockedUntil > time.Now().Unix() {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(c.Password)) // equal timing
		if err == nil && !u.IsGuest {
			s.logEvent(u.ID, u.Username, "login_failed", "", "account locked")
		}
		unlock()
		rejectLogin(w)
		return
	}
	// QueryRow has released its connection before the expensive password comparison.
	matched := bcrypt.CompareHashAndPassword([]byte(u.passwordHash), []byte(c.Password)) == nil
	now := time.Now().Unix()
	failures, lockedUntil := int64(0), int64(0)
	// A correct password only completes the first step when a second factor is
	// due: the failure counter is then kept (and only reset after MFA succeeds).
	needMFA := matched && u.EmailVerified && s.mfaGate(u)
	if needMFA {
		failures, lockedUntil = u.loginFailures, u.loginLockedUntil
		if lockedUntil != 0 && lockedUntil <= now {
			failures, lockedUntil = 0, 0
		}
	}
	if !matched {
		failures = u.loginFailures + 1
		if u.loginLockedUntil != 0 && u.loginLockedUntil <= now {
			failures = 1
		}
		if failures >= 5 {
			lockedUntil = now + int64((15*time.Minute)/time.Second)
		}
	}
	// A concurrent identity/credential mutation must not inherit an old attempt.
	res, err := s.St.DB.Exec(`UPDATE users SET login_failures=?,login_locked_until=? WHERE id=? AND password_hash=? AND email=? AND must_change_password=? AND is_guest=0`, failures, lockedUntil, u.ID, u.passwordHash, u.Email, u.MustChangePassword)
	if err != nil {
		unlock()
		rejectLogin(w)
		return
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 || !matched {
		if !matched {
			s.logEvent(u.ID, u.Username, "login_failed", "", "")
			if lockedUntil > 0 {
				s.logEvent(u.ID, u.Username, "login_lockout", "", "locked after repeated failures")
			}
		}
		unlock()
		rejectLogin(w)
		return
	}
	s.rehashPassword(u, c.Password) // upgrade an older, cheaper hash before the credential version is used
	if !u.EmailVerified {
		jout(w, 403, map[string]any{"error": "email not verified", "needs_verification": true})
		return
	}
	if needMFA {
		s.mfaChallenge(w, u)
		return
	}
	s.logEvent(u.ID, u.Username, "login_ok", "", "")
	s.sessionResponse(w, r, u)
}

// refreshReuseGrace: a rotated refresh token presented again within this time is
// treated as a race (two tabs refreshing at once, a response lost on the way)
// and only refused; later reuse means the token was copied.
const refreshReuseGrace = 60 * time.Second

func (s *Server) clearRefreshCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "refresh", Value: "", Path: "/api/auth", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secureCookies(r)})
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("refresh")
	if err != nil {
		jerr(w, 401, "no session")
		return
	}
	idleMinutes := s.idleLogoutMinutes() // read before the transaction takes the only connection
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	// end refuses the refresh, keeping the token deleted, and clears the cookie.
	end := func() {
		tx.Commit()
		s.clearRefreshCookie(w, r)
		jerr(w, 401, "session expired")
	}
	var uid int64
	var issued, started sql.NullTime
	if err := tx.QueryRow(`DELETE FROM refresh_tokens WHERE hash=? AND expires_at>? RETURNING user_id,issued_at,session_started`, hashToken(c.Value), time.Now()).Scan(&uid, &issued, &started); err != nil {
		var victim int64
		var spentAt time.Time
		if tx.QueryRow(`SELECT user_id,spent_at FROM refresh_spent WHERE hash=? AND expires_at>?`, hashToken(c.Value), time.Now()).Scan(&victim, &spentAt) == nil {
			if time.Since(spentAt) < refreshReuseGrace {
				// Probably a parallel refresh from another tab: refuse without
				// touching the cookie, which may already hold the new token.
				jerr(w, 401, "session expired")
				return
			}
			// A rotated token presented again later means it was copied: revoke
			// the whole session family so the copy stops working too.
			tx.Exec(`DELETE FROM refresh_tokens WHERE user_id=?`, victim)
			tx.Exec(`DELETE FROM refresh_spent WHERE user_id=?`, victim)
			if tx.Commit() == nil {
				s.logEvent(victim, s.usernameOf(victim), "session_reuse_detected", "", "all sessions signed out")
			}
		}
		// Release the only connection before clearRefreshCookie reads settings.
		tx.Rollback()
		s.clearRefreshCookie(w, r)
		jerr(w, 401, "session expired")
		return
	}
	now := time.Now()
	start, authTime := now, time.Time{}
	if started.Valid {
		start, authTime = started.Time, started.Time
	} else if issued.Valid {
		start = issued.Time // older token: cap the session, but no recent sign-in
	}
	if now.Sub(start) >= sessionMaxAge {
		end()
		return
	}
	u, err := scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, uid))
	if err != nil {
		end()
		return
	}
	if u.IsGuest {
		var valid int
		if tx.QueryRow(`SELECT COUNT(*) FROM reverse_invites WHERE guest_user_id=? AND completed_at IS NULL AND expires_at>?`, uid, time.Now()).Scan(&valid) != nil || valid != 1 {
			end()
			return
		}
	} else {
		if u.passwordHash == "" {
			// An account without a local password exists only through its sign-in
			// provider: when the link is removed or the provider is disabled, the
			// session ends at the next refresh.
			var links int
			if tx.QueryRow(`SELECT COUNT(*) FROM user_auth_providers upa JOIN auth_providers ap ON ap.id=upa.provider_id WHERE upa.user_id=? AND ap.enabled=1`, uid).Scan(&links) != nil || links == 0 {
				end()
				return
			}
		}
		if issued.Valid && idleExpired(idleMinutes, issued.Time, time.Now()) {
			end() // abandoned session
			return
		}
	}
	// Only a successful rotation records the old token as spent.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO refresh_spent(hash,user_id,spent_at,expires_at) VALUES(?,?,?,?)`, hashToken(c.Value), uid, now, start.Add(sessionMaxAge)); err != nil {
		jerr(w, 500, "session failed")
		return
	}
	rt, exp := randHex(32), start.Add(sessionMaxAge)
	if _, err := tx.Exec(`INSERT INTO refresh_tokens(hash,user_id,expires_at,issued_at,session_started) VALUES(?,?,?,?,?)`, hashToken(rt), uid, exp, now, start); err != nil {
		jerr(w, 500, "session failed")
		return
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "session failed")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "refresh", Value: rt, Path: "/api/auth", Expires: exp, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secureCookies(r)})
	s.fillMFA(u) // the transaction above has committed and released the connection
	jout(w, 200, map[string]any{"access_token": s.accessTokenAt(u, authTime), "user": u})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims := jwt.MapClaims{}
	if tok != "" {
		if _, err := jwt.ParseWithClaims(tok, claims, func(*jwt.Token) (any, error) { return s.key(keyAccess), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired()); err == nil && claims["purpose"] == "access" {
			if exp, err := claims.GetExpirationTime(); err == nil && exp != nil {
				s.St.DB.Exec(`INSERT OR IGNORE INTO revoked_access_tokens(hash,expires_at) VALUES(?,?)`, hashToken(tok), exp.Time)
			}
		}
	}
	if c, err := r.Cookie("refresh"); err == nil {
		s.St.DB.Exec(`DELETE FROM refresh_tokens WHERE hash=?`, hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "refresh", Path: "/api/auth", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secureCookies(r)})
	jout(w, 200, map[string]any{"ok": true})
}

// ---------- password reset ----------

func (s *Server) forgotPassword(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalLogin(w) {
		return
	}
	var c credentials
	decode(r, &c)
	// always respond the same to avoid user enumeration
	defer jout(w, 200, map[string]any{"ok": true})
	email, err := canonicalEmail(c.Email)
	if err != nil || email == "" {
		return
	}
	u, err := scanUser(s.St.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE lower(trim(email))=? AND email<>'' AND email_verified=1 AND is_guest=0
		AND (SELECT COUNT(*) FROM users WHERE lower(trim(email))=?)=1`, email, email))
	if err != nil || !s.allowRequest("reset:"+strconv.FormatInt(u.ID, 10), 1, time.Minute) {
		return
	}
	base, ok := s.emailBaseURL(r)
	if !ok {
		return
	}
	tok := randHex(24)
	res, err := s.St.DB.Exec(`INSERT INTO password_resets(hash,user_id,expires_at)
		SELECT ?,id,? FROM users WHERE id=? AND email=? AND password_hash=? AND password_hash<>'' AND email_verified=1
		`, hashToken(tok), time.Now().Add(time.Hour), u.ID, u.Email, u.passwordHash)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return
	}
	url := base + "/reset-password/" + tok
	go func() {
		if err := s.deliverTemplate(u.Email, "tpl_password_reset", map[string]any{"URL": url}); err != nil {
			s.St.DB.Exec(`DELETE FROM password_resets WHERE hash=?`, hashToken(tok))
		}
	}()
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalLogin(w) {
		return
	}
	var b struct{ Token, Password string }
	if decode(r, &b) != nil {
		jerr(w, 400, "invalid request")
		return
	}
	if !s.requireAccountPassword(w, b.Password) {
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(b.Password), bcryptCost)
	if err != nil {
		jerr(w, 400, "invalid password")
		return
	}
	var uid int64
	if err := s.St.DB.QueryRow(`SELECT user_id FROM password_resets WHERE hash=? AND expires_at>?`, hashToken(b.Token), time.Now()).Scan(&uid); err != nil {
		jerr(w, 400, "invalid or expired token")
		return
	}
	unlock := lockStripe(&s.loginLocks, strconv.FormatInt(uid, 10))
	defer unlock()
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	var kind string
	if err := tx.QueryRow(`DELETE FROM password_resets WHERE hash=? AND expires_at>? AND user_id=? RETURNING user_id, kind`, hashToken(b.Token), time.Now(), uid).Scan(&uid, &kind); err != nil {
		jerr(w, 400, "invalid or expired token")
		return
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE users SET password_hash=?, must_change_password=0, login_failures=0, login_locked_until=0 WHERE id=? AND is_guest=0`, []any{string(hash), uid}},
		{`DELETE FROM password_resets WHERE user_id=?`, []any{uid}},
		{`DELETE FROM refresh_tokens WHERE user_id=?`, []any{uid}},
	} {
		if _, err := tx.Exec(q.sql, q.args...); err != nil {
			jerr(w, 500, "password reset failed")
			return
		}
	}
	if kind == "welcome" {
		// The link only reached the mailbox owner, which proves the address.
		if _, err := tx.Exec(`UPDATE users SET email_verified=1,pending_registration=0 WHERE id=? AND is_guest=0 AND email<>''`, uid); err != nil {
			jerr(w, 500, "password reset failed")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "password reset failed")
		return
	}
	jout(w, 200, map[string]any{"ok": true})
}

// ---------- self registration ----------

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalLogin(w) {
		return
	}
	if !s.St.SettingBool("allow_self_registration") {
		jerr(w, 403, "registration disabled")
		return
	}
	var c credentials
	if decode(r, &c) != nil || c.Username == "" {
		jerr(w, 400, "username, email and password required")
		return
	}
	if !validUsername(c.Username) {
		jerr(w, 400, msgInvalidUsername)
		return
	}
	if !s.requireAccountPassword(w, c.Password) {
		return
	}
	email, err := canonicalEmail(c.Email)
	if err != nil || email == "" {
		jerr(w, 400, "valid email required")
		return
	}
	domains, derr := s.registrationDomainList()
	if derr != nil {
		jerr(w, 500, "registration temporarily unavailable")
		return
	}
	if !domainAllowed(domains, email) {
		jerr(w, 400, "registration is not available for this email domain")
		return
	}
	c.Email = email
	if c.Language == "" {
		c.Language = "en"
	}
	if !s.St.ValidMailLanguage(c.Language) {
		jerr(w, 400, "unsupported language")
		return
	}
	if c.UILanguage == "" {
		c.UILanguage = "en"
	}
	if !s.St.ValidUILanguage(c.UILanguage) {
		jerr(w, 400, "unsupported UI language")
		return
	}
	// Public registration always verifies ownership; legacy settings cannot bypass it.
	verify := true
	if verify {
		if err := s.smtpConfigured(); err != nil {
			jerr(w, 503, "registration temporarily unavailable: email is not configured")
			return
		}
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(c.Password), bcryptCost)
	res, err := s.St.DB.Exec(`INSERT INTO users(username,name,email,password_hash,email_verified,language,ui_language,pending_registration) VALUES(?,?,?,?,?,?,?,?)`,
		c.Username, c.Name, c.Email, string(hash), !verify, c.Language, c.UILanguage, verify)
	if err != nil {
		// Usernames are visible to share recipients anyway, so a taken username is
		// reported. A taken email address gets the same answer as a successful
		// registration, so registration cannot reveal who has an account.
		var taken int
		s.St.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE username=? COLLATE NOCASE`, c.Username).Scan(&taken)
		if taken > 0 {
			jerr(w, 400, "username already in use")
			return
		}
		if email, _ := canonicalEmail(c.Email); email != "" && !s.emailAvailable(email, 0) {
			jout(w, 201, map[string]any{"needs_verification": true})
			return
		}
		jerr(w, 500, "registration failed")
		return
	}
	id, _ := res.LastInsertId()
	u, err := s.userByID(id)
	if err != nil {
		jerr(w, 500, "user policy unavailable")
		return
	}
	if verify {
		s.sendVerification(u)
		jout(w, 201, map[string]any{"needs_verification": true})
		return
	}
	s.sessionResponse(w, r, u)
}

func (s *Server) sendVerification(u *User) {
	if !s.allowRequest("verification-mail:"+strconv.FormatInt(u.ID, 10)+":"+u.Email, 1, time.Minute) {
		return
	}
	n, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		return
	}
	code := fmt.Sprintf("%08d", n.Int64())
	s.St.DB.Exec(`INSERT INTO email_verifications(user_id,code,expires_at) VALUES(?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET code=excluded.code, expires_at=excluded.expires_at`, u.ID, code, time.Now().Add(30*time.Minute))
	s.queueMail(u.Email, "Verify your email", "tpl_verify_email", map[string]any{"Code": code})
}

func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalLogin(w) {
		return
	}
	var b struct{ Email, Code string }
	if decode(r, &b) != nil {
		jerr(w, 400, "bad request")
		return
	}
	email, err := canonicalEmail(b.Email)
	if err != nil || email == "" {
		jerr(w, 400, "invalid or expired code")
		return
	}
	b.Email = email
	unlock := lockStripe(&s.verificationLocks, b.Email)
	defer unlock()
	var uid int64
	var code string
	var exp time.Time
	var pending bool
	err = s.St.DB.QueryRow(`SELECT v.user_id,v.code,v.expires_at,u.pending_registration FROM email_verifications v JOIN users u ON u.id=v.user_id WHERE lower(trim(u.email))=? AND u.email_verified=0 AND u.is_guest=0 AND (SELECT COUNT(*) FROM users WHERE lower(trim(email))=?)=1`, b.Email, b.Email).Scan(&uid, &code, &exp, &pending)
	if err != nil || time.Now().After(exp) {
		jerr(w, 400, "invalid or expired code")
		return
	}
	var attempts int
	err = s.St.DB.QueryRow(`INSERT INTO verification_attempts(user_id,attempts,expires_at) VALUES(?,1,?)
		ON CONFLICT(user_id) DO UPDATE SET
		attempts=CASE WHEN verification_attempts.expires_at<=? THEN 1 ELSE verification_attempts.attempts+1 END,
		expires_at=CASE WHEN verification_attempts.expires_at<=? THEN excluded.expires_at ELSE verification_attempts.expires_at END
		RETURNING attempts`, uid, time.Now().Add(30*time.Minute), time.Now(), time.Now()).Scan(&attempts)
	if err != nil || attempts > 5 {
		jerr(w, 429, "too many verification attempts; try again later")
		return
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(strings.TrimSpace(b.Code))) != 1 {
		jerr(w, 400, "invalid or expired code")
		return
	}
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE users SET email_verified=1,pending_registration=0 WHERE id=? AND lower(trim(email))=? AND email_verified=0 AND EXISTS(SELECT 1 FROM email_verifications WHERE user_id=? AND code=? AND expires_at>?)`, uid, b.Email, uid, code, time.Now())
	if err != nil {
		jerr(w, 500, "verification failed")
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		jerr(w, 400, "invalid or expired code")
		return
	}
	for _, table := range []string{"verification_attempts", "email_verifications"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE user_id=?`, uid); err != nil {
			jerr(w, 500, "verification failed")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "verification failed")
		return
	}
	// Only a fresh self-registration is signed in by proving the mailbox. Any
	// other account (for example one whose address was changed later) must sign
	// in with its password: possession of the mailbox is not a login.
	if !pending {
		jout(w, 200, map[string]any{"ok": true, "verified": true})
		return
	}
	u, _ := s.userByID(uid)
	if u == nil {
		jerr(w, 500, "user policy unavailable")
		return
	}
	s.sessionOrChallenge(w, r, u)
}

func (s *Server) resendVerification(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalLogin(w) {
		return
	}
	var b struct{ Email string }
	decode(r, &b)
	email, err := canonicalEmail(b.Email)
	if err == nil && email != "" {
		if u, err := scanUser(s.St.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE lower(trim(email))=? AND email_verified=0 AND is_guest=0 AND (SELECT COUNT(*) FROM users WHERE lower(trim(email))=?)=1`, email, email)); err == nil {
			s.sendVerification(u)
		}
	}
	jout(w, 200, map[string]any{"ok": true})
}

// ---------- profile ----------

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := s.ctxUser(r)
	rows, _ := s.St.DB.Query(`SELECT p.id,p.name,p.type FROM user_auth_providers up JOIN auth_providers p ON p.id=up.provider_id WHERE up.user_id=?`, u.ID)
	linked := []map[string]any{}
	if rows != nil {
		for rows.Next() {
			var id int64
			var name, typ string
			rows.Scan(&id, &name, &typ)
			linked = append(linked, map[string]any{"id": id, "name": name, "type": typ})
		}
		rows.Close()
	}
	usage, err := s.St.UserStorageUsage(u.ID)
	if err != nil {
		jerr(w, 500, "storage usage unavailable")
		return
	}
	u.QuotaBytes, u.QuotaInheritsDefault = usage.QuotaBytes, usage.QuotaInheritsDefault
	s.fillMFA(u)
	jout(w, 200, map[string]any{"version": s.Version, "user": u, "providers": linked, "has_password": u.passwordHash != "", "quota_inherits_default": usage.QuotaInheritsDefault, "quota_bytes": usage.QuotaBytes, "storage_used_bytes": usage.StoredBytes, "storage_reserved_bytes": usage.ReservedBytes, "storage_available_bytes": usage.AvailableBytes})
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) {
	u := s.ctxUser(r)
	if u.IsGuest {
		jerr(w, 403, "guest session is upload-only")
		return
	}
	var b struct {
		Name            string  `json:"name"`
		Email           string  `json:"email"`
		Password        string  `json:"password"`
		CurrentPassword string  `json:"current_password"`
		Language        *string `json:"language"`
		UILanguage      *string `json:"ui_language"`
	}
	if decode(r, &b) != nil {
		jerr(w, 400, "bad request")
		return
	}
	unlock := lockStripe(&s.loginLocks, strconv.FormatInt(u.ID, 10))
	defer unlock()
	email, err := canonicalEmail(b.Email)
	if err != nil {
		jerr(w, 400, "invalid email")
		return
	}
	if email != u.Email {
		// A self-service address change must not bypass the registration domain list.
		domains, derr := s.registrationDomainList()
		if derr != nil {
			jerr(w, 500, "registration temporarily unavailable")
			return
		}
		if !domainAllowed(domains, email) {
			jerr(w, 400, "this email domain is not allowed")
			return
		}
	}
	language := u.Language
	if b.Language != nil {
		language = *b.Language
		if language == "" {
			language = "en"
		}
		if !s.St.ValidMailLanguage(language) {
			jerr(w, 400, "unsupported language")
			return
		}
	}
	uiLanguage := u.UILanguage
	if b.UILanguage != nil {
		uiLanguage = *b.UILanguage
		if !s.St.ValidUILanguage(uiLanguage) {
			jerr(w, 400, "unsupported UI language")
			return
		}
	}
	emailChanged := email != strings.ToLower(strings.TrimSpace(u.Email))
	if !s.emailAvailable(email, u.ID) {
		jerr(w, 400, "email already in use")
		return
	}
	if (b.Password != "" || emailChanged) && u.passwordHash != "" && !u.MustChangePassword {
		if s.stepUpLocked(w, u) {
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(u.passwordHash), []byte(b.CurrentPassword)) != nil {
			s.stepUpFailed(w, u)
			jerr(w, 400, "current password incorrect")
			return
		}
	}
	// Accounts without a local password cannot confirm with one: require a recent
	// sign-in through their provider instead, so a stolen access token cannot add
	// a permanent local password or redirect the email.
	if (b.Password != "" || emailChanged) && u.passwordHash == "" && time.Since(s.requestAuthTime(r)) > recentSignIn {
		jerr(w, 403, msgRecentSignIn)
		return
	}
	passwordHash := u.passwordHash
	mustChange := u.MustChangePassword
	if b.Password != "" {
		if !s.requireAccountPassword(w, b.Password) {
			return
		}
		h, err := bcrypt.GenerateFromPassword([]byte(b.Password), bcryptCost)
		if err != nil {
			jerr(w, 400, "invalid password")
			return
		}
		passwordHash, mustChange = string(h), false
	}
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	verified := u.EmailVerified
	if emailChanged {
		verified = email == ""
	}
	res, err := tx.Exec(`UPDATE users SET name=?,email=?,email_verified=?,password_hash=?,must_change_password=?,language=?,ui_language=COALESCE(?,ui_language),login_failures=CASE WHEN ? THEN 0 ELSE login_failures END,login_locked_until=CASE WHEN ? THEN 0 ELSE login_locked_until END WHERE id=? AND password_hash=? AND email=? AND must_change_password=?`, b.Name, email, verified, passwordHash, mustChange, language, b.UILanguage, b.Password != "", b.Password != "", u.ID, u.passwordHash, u.Email, u.MustChangePassword)
	if err != nil {
		jerr(w, 400, "profile update failed")
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		jerr(w, 409, "profile changed; retry")
		return
	}
	if emailChanged || b.Password != "" {
		for _, table := range []string{"refresh_tokens", "password_resets", "email_verifications"} {
			if _, err := tx.Exec(`DELETE FROM `+table+` WHERE user_id=?`, u.ID); err != nil {
				jerr(w, 500, "profile update failed")
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "profile update failed")
		return
	}
	nu, err := s.userByID(u.ID)
	if err != nil {
		jerr(w, 500, "profile update failed")
		return
	}
	if emailChanged && email != "" {
		s.sendVerification(nu)
	}
	if emailChanged || b.Password != "" {
		s.issueSessionSince(w, r, nu, s.sessionStartOf(r))
	}
	s.fillMFA(nu)
	jout(w, 200, map[string]any{"user": nu, "access_token": s.accessTokenAt(nu, s.requestAuthTime(r))})
}

func (s *Server) unlinkProvider(w http.ResponseWriter, r *http.Request) {
	u := s.ctxUser(r)
	pid, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var links int
	s.St.DB.QueryRow(`SELECT COUNT(*) FROM user_auth_providers WHERE user_id=?`, u.ID).Scan(&links)
	if s.localLoginStoredOff() {
		// A password cannot sign in while local login is off: keep another enabled external link.
		var others int
		s.St.DB.QueryRow(`SELECT COUNT(*) FROM user_auth_providers up JOIN auth_providers p ON p.id=up.provider_id
			WHERE up.user_id=? AND p.enabled=1 AND p.id<>?`, u.ID, pid).Scan(&others)
		var mine int
		s.St.DB.QueryRow(`SELECT COUNT(*) FROM user_auth_providers up JOIN auth_providers p ON p.id=up.provider_id
			WHERE up.user_id=? AND p.enabled=1 AND p.id=?`, u.ID, pid).Scan(&mine)
		if mine > 0 && others == 0 {
			jerr(w, 400, msgUnlinkLockout)
			return
		}
	} else if u.passwordHash == "" && links <= 1 {
		jerr(w, 400, "set a password before unlinking your only login method")
		return
	}
	s.St.DB.Exec(`DELETE FROM user_auth_providers WHERE user_id=? AND provider_id=?`, u.ID, pid)
	jout(w, 200, map[string]any{"ok": true})
}

// ---------- admin: users ----------

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.St.DB.Query(`SELECT ` + userCols + ` FROM users WHERE is_guest=0 ORDER BY username`)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	users := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			rows.Close()
			jerr(w, 500, "user list unavailable")
			return
		}
		users = append(users, u)
	}
	err = rows.Err()
	rows.Close() // Release the single pooled connection before querying usage.
	if err != nil {
		jerr(w, 500, "user list unavailable")
		return
	}
	// Where each account comes from: a local password and/or linked providers.
	sources := map[int64][]string{}
	if lr, lerr := s.St.DB.Query(`SELECT upa.user_id, ap.type FROM user_auth_providers upa JOIN auth_providers ap ON ap.id=upa.provider_id ORDER BY ap.type`); lerr == nil {
		for lr.Next() {
			var uid int64
			var typ string
			if lr.Scan(&uid, &typ) == nil {
				if l := sources[uid]; len(l) == 0 || l[len(l)-1] != typ {
					sources[uid] = append(l, typ)
				}
			}
		}
		lr.Close()
	}
	type adminUser struct {
		*User
		Sources []string `json:"sources"`
		*store.StorageUsage
		QuotaBytes           int64 `json:"quota_bytes"`
		QuotaInheritsDefault bool  `json:"quota_inherits_default"`
		LoginLockedUntil     int64 `json:"login_locked_until"`
	}
	out := []adminUser{}
	for _, u := range users {
		usage, err := s.St.UserStorageUsage(u.ID)
		if err != nil {
			jerr(w, 500, "storage usage unavailable")
			return
		}
		s.fillMFA(u)
		src := []string{}
		if u.passwordHash != "" {
			src = append(src, "local")
		}
		src = append(src, sources[u.ID]...)
		out = append(out, adminUser{User: u, Sources: src, StorageUsage: usage, QuotaBytes: usage.QuotaBytes, QuotaInheritsDefault: usage.QuotaInheritsDefault, LoginLockedUntil: u.loginLockedUntil})
	}
	jout(w, 200, out)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var b struct {
		credentials
		Admin                bool   `json:"admin"`
		Auditor              bool   `json:"auditor"`
		MustChangePassword   bool   `json:"must_change_password"`
		QuotaBytes           *int64 `json:"quota_bytes"`
		QuotaInheritsDefault *bool  `json:"quota_inherits_default"`
		SendWelcome          bool   `json:"send_welcome"`
	}
	if decode(r, &b) != nil || b.Username == "" {
		jerr(w, 400, "username and password required")
		return
	}
	if !validUsername(b.Username) {
		jerr(w, 400, msgInvalidUsername)
		return
	}
	if b.Admin && b.Auditor {
		jerr(w, 400, msgAdminAuditorExclusive)
		return
	}
	if !s.requireAccountPassword(w, b.Password) {
		return
	}
	email, err := canonicalEmail(b.Email)
	if err != nil {
		jerr(w, 400, "invalid email")
		return
	}
	b.Email = email
	if b.Language == "" {
		b.Language = "en"
	}
	if !s.St.ValidMailLanguage(b.Language) {
		jerr(w, 400, "unsupported language")
		return
	}
	if b.UILanguage == "" {
		b.UILanguage = "en"
	}
	if !s.St.ValidUILanguage(b.UILanguage) {
		jerr(w, 400, "unsupported UI language")
		return
	}
	if _, ok := quotaChoice(b.QuotaBytes, b.QuotaInheritsDefault); !ok {
		jerr(w, 400, "invalid quota choice")
		return
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(b.Password), bcryptCost)
	q := `INSERT INTO users(username,name,email,password_hash,admin,must_change_password,language,ui_language) VALUES(?,?,?,?,?,?,?,?)`
	args := []any{b.Username, b.Name, b.Email, string(h), b.Admin, b.MustChangePassword, b.Language, b.UILanguage}
	if b.QuotaBytes != nil {
		q = `INSERT INTO users(username,name,email,password_hash,admin,must_change_password,language,ui_language,quota_bytes,quota_inherits_default) VALUES(?,?,?,?,?,?,?,?,?,0)`
		args = append(args, *b.QuotaBytes)
	}
	res, err := s.St.DB.Exec(q, args...)
	if err != nil {
		jerr(w, 400, "username already exists")
		return
	}
	id, _ := res.LastInsertId()
	if b.Auditor {
		if _, err := s.St.DB.Exec(`UPDATE users SET is_auditor=1 WHERE id=? AND admin=0`, id); err != nil {
			s.St.DB.Exec(`DELETE FROM users WHERE id=?`, id)
			jerr(w, 500, "user creation failed")
			return
		}
	}
	u, err := s.userByID(id)
	if err != nil {
		jerr(w, 500, "user policy unavailable")
		return
	}
	me := s.ctxUser(r)
	out := struct {
		*User
		WelcomeEmail string `json:"welcome_email,omitempty"`
		WelcomeNote  string `json:"welcome_note,omitempty"`
	}{User: u}
	detail := ""
	if b.SendWelcome {
		// The user is already created: a failed mail must never undo that.
		out.WelcomeEmail, out.WelcomeNote = s.sendWelcome(r, me, u)
		detail = "welcome=" + out.WelcomeEmail
	}
	s.logEvent(me.ID, me.Username, "user_created", s.userTarget(id), detail)
	jout(w, 201, out)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var b struct {
		Name, Email          *string
		Password             string
		Admin                *bool   `json:"admin"` // omitted = unchanged
		Auditor              *bool   `json:"auditor"`
		QuotaBytes           *int64  `json:"quota_bytes"`
		QuotaInheritsDefault *bool   `json:"quota_inherits_default"`
		MFAPolicy            *string `json:"mfa_policy"`
	}
	if decode(r, &b) != nil {
		jerr(w, 400, "bad request")
		return
	}
	inheritance, ok := quotaChoice(b.QuotaBytes, b.QuotaInheritsDefault)
	if !ok {
		jerr(w, 400, "invalid quota choice")
		return
	}
	if b.MFAPolicy != nil && !validMFAUserPolicy(*b.MFAPolicy) {
		jerr(w, 400, "mfa_policy must be inherit, required or exempt")
		return
	}
	me := s.ctxUser(r)
	if b.MFAPolicy != nil && *b.MFAPolicy == "required" && id == me.ID && !s.userHasFactor(me) {
		jerr(w, 400, msgMFAEnforceSelf)
		return
	}
	if id == me.ID && b.Admin != nil && !*b.Admin {
		jerr(w, 400, "cannot remove your own admin rights")
		return
	}
	if b.Password != "" && id != me.ID {
		// An administrator who could set a password could sign in as the user and
		// read their shares. The user chooses their own password through a link
		// that only reaches their mailbox.
		jerr(w, 400, msgAdminNoPassword)
		return
	}
	if b.Password != "" && !s.requireAccountPassword(w, b.Password) {
		return
	}
	unlock := lockStripe(&s.loginLocks, strconv.FormatInt(id, 10))
	defer unlock()
	target, err := s.userByID(id)
	if err != nil || target.IsGuest {
		jerr(w, 404, "not found")
		return
	}
	name, email := target.Name, target.Email
	if b.Name != nil {
		name = *b.Name
	}
	if b.Email != nil {
		var err error
		if email, err = canonicalEmail(*b.Email); err != nil || !s.emailAvailable(email, id) {
			jerr(w, 400, "invalid or already used email")
			return
		}
	}
	admin := target.Admin
	if b.Admin != nil {
		admin = *b.Admin
	}
	if target.Admin && !admin && s.refuseLastExternalAdmin(w, id, 0) {
		return
	}
	// Another user's address changed by an administrator is unverified again, so
	// the administrator cannot use it to take over the account.
	emailChangedByAdmin := id != me.ID && !strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(target.Email))
	newAuditor := target.Auditor
	if b.Auditor != nil {
		newAuditor = *b.Auditor
	}
	if admin && newAuditor {
		jerr(w, 400, msgAdminAuditorExclusive)
		return
	}
	hash := target.passwordHash
	if b.Password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(b.Password), bcryptCost)
		if err != nil {
			jerr(w, 400, "invalid password")
			return
		}
		hash = string(h)
	}
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE users SET name=?,email=?,admin=?,password_hash=?,quota_bytes=COALESCE(?,quota_bytes),quota_inherits_default=COALESCE(?,quota_inherits_default),mfa_policy=COALESCE(?,mfa_policy),login_failures=CASE WHEN ? THEN 0 ELSE login_failures END,login_locked_until=CASE WHEN ? THEN 0 ELSE login_locked_until END,email_verified=CASE WHEN ? THEN 0 ELSE email_verified END WHERE id=? AND is_guest=0 AND password_hash=? AND email=? AND must_change_password=?`, name, email, admin, hash, b.QuotaBytes, inheritance, b.MFAPolicy, b.Password != "", b.Password != "", emailChangedByAdmin, id, target.passwordHash, target.Email, target.MustChangePassword)
	if err != nil {
		jerr(w, 400, "user update failed")
		return
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		jerr(w, 409, "user changed; retry")
		return
	}
	if _, err := tx.Exec(`UPDATE users SET is_auditor=? WHERE id=?`, newAuditor, id); err != nil {
		jerr(w, 500, "user update failed")
		return
	}
	if b.Password != "" || email != target.Email {
		for _, table := range []string{"refresh_tokens", "password_resets", "email_verifications", "mfa_reset_requests"} {
			if _, err := tx.Exec(`DELETE FROM `+table+` WHERE user_id=?`, id); err != nil {
				jerr(w, 500, "user update failed")
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "user update failed")
		return
	}
	u, err := s.userByID(id)
	if err == sql.ErrNoRows {
		jerr(w, 404, "not found")
		return
	}
	if err != nil {
		jerr(w, 500, "user policy unavailable")
		return
	}
	me2 := s.ctxUser(r)
	detail := ""
	if b.Password != "" {
		detail = "password changed"
	}
	s.logEvent(me2.ID, me2.Username, "user_updated", s.userTarget(id), detail)
	if emailChangedByAdmin {
		s.logEvent(me2.ID, me2.Username, "user_email_changed", s.userTarget(id), "email_verified reset")
		s.notifyEmailChanged(target)
	}
	if target.Admin != admin || target.Auditor != newAuditor {
		s.logEvent(me2.ID, me2.Username, "user_role_changed", s.userTarget(id), "admin="+strconv.FormatBool(admin)+" auditor="+strconv.FormatBool(newAuditor))
	}
	if b.MFAPolicy != nil && *b.MFAPolicy != target.MFAPolicy {
		s.logEvent(me2.ID, me2.Username, "mfa_policy_changed", s.userTarget(id), "policy="+*b.MFAPolicy)
	}
	s.fillMFA(u)
	jout(w, 200, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if id == s.ctxUser(r).ID {
		jerr(w, 400, "cannot delete yourself")
		return
	}
	if s.refuseLastExternalAdmin(w, id, 0) {
		return
	}
	deletedLabel := s.userTarget(id) // resolve the name before the account disappears
	unlock := lockStripe(&s.loginLocks, strconv.FormatInt(id, 10))
	defer unlock()
	unlockUploads := s.LockUserUploads(id)
	defer unlockUploads()
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "user deletion failed")
		return
	}
	defer tx.Rollback()
	// Take the database write lock before discovery, so admissions cannot slip in.
	res, err := tx.Exec(`UPDATE users SET id=id WHERE id=? AND is_guest=0`, id)
	if err != nil {
		jerr(w, 500, "user deletion failed")
		return
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		jerr(w, 404, "not found")
		return
	}
	var dirs []string
	for _, q := range []struct{ sql, kind string }{
		{`SELECT id FROM upload_sessions WHERE user_id=? OR quota_owner_id=? OR user_id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=?)`, "uploads"},
		{`SELECT long_id FROM shares WHERE user_id=? OR user_id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=?) OR invite_id IN (SELECT id FROM reverse_invites WHERE user_id=?)`, "shares"},
	} {
		rows, err := tx.Query(q.sql, id, id, id)
		if err != nil {
			jerr(w, 500, "user deletion failed")
			return
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				jerr(w, 500, "user deletion failed")
				return
			}
			if q.kind == "shares" {
				dir, derr := store.ShareDir(s.St.DataDir, name)
				if derr != nil {
					continue // malformed long ID: nothing safe to remove
				}
				dirs = append(dirs, dir)
				continue
			}
			dirs = append(dirs, s.uploadDir(name))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			jerr(w, 500, "user deletion failed")
			return
		}
	}
	for _, q := range []string{
		`DELETE FROM upload_sessions WHERE user_id=? OR quota_owner_id=? OR user_id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=?)`,
		`DELETE FROM shares WHERE user_id=? OR user_id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=?) OR invite_id IN (SELECT id FROM reverse_invites WHERE user_id=?)`,
		`DELETE FROM users WHERE is_guest=1 AND id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=?)`,
		`DELETE FROM users WHERE id=?`,
	} {
		args := []any{id}
		if strings.Count(q, "?") == 3 {
			args = []any{id, id, id}
		}
		if _, err := tx.Exec(q, args...); err != nil {
			jerr(w, 500, "user deletion failed")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "user deletion failed")
		return
	}
	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			jerr(w, 500, "user deleted but file cleanup failed")
			return
		}
	}
	me := s.ctxUser(r)
	s.logEvent(me.ID, me.Username, "user_deleted", deletedLabel, "")
	jout(w, 200, map[string]any{"ok": true})
}

func (s *Server) forceReset(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	unlock := lockStripe(&s.loginLocks, strconv.FormatInt(id, 10))
	defer unlock()
	res, err := s.St.DB.Exec(`UPDATE users SET must_change_password=1 WHERE id=? AND is_guest=0`, id)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		jerr(w, 404, "not found")
		return
	}
	s.St.DB.Exec(`DELETE FROM refresh_tokens WHERE user_id=?`, id)
	me := s.ctxUser(r)
	// The user chooses the new password through a link that only reaches their
	// verified mailbox; the administrator never sees the link.
	outcome, note := s.sendAdminPasswordReset(r, me, id)
	s.logEvent(me.ID, me.Username, "user_force_reset", s.userTarget(id), "")
	s.logEvent(me.ID, me.Username, "user_password_reset_sent", s.userTarget(id), "reset_email="+outcome)
	out := map[string]any{"ok": true, "reset_email": outcome}
	if note != "" {
		out["reset_note"] = note
	}
	jout(w, 200, out)
}
