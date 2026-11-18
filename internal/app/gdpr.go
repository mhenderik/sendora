package app

import (
	"database/sql"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mhenderik/sendora/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Personal data: a self-service export (GDPR art. 15/20) and erasure of an
// account (art. 17), by the user or by an administrator.
//
// Erasure deletes the account's shares and files, upload sessions, invites
// (and their guest accounts), sessions, reset and verification codes, second
// factors, linked sign-ins and sign-in fingerprints, and pseudonymises the
// account row: username "erased-<id>", name and email cleared, no password, no
// roles. The row stays so that references (ids) keep resolving; it can never
// sign in again.
//
// Legitimate-interest exception: audit records written while the account
// existed keep their actor_name (the former username) and are not rewritten.
// The audit log is tamper-evident (hash chain) and serves the security of the
// service and the accountability of administrators (GDPR art. 6(1)(f), and
// art. 17(3)(e) for the establishment or defence of legal claims). Those
// records disappear with the normal retention (audit_retention_days, at most
// 365 days). See docs/THREAT-MODEL.md.

const (
	msgEraseLastAdmin  = "the last administrator cannot be erased; make another account administrator first"
	msgEraseSelfAdmin  = "you cannot erase your own account here; use Profile -> Delete my account"
	msgEraseRecent     = "sign in again to delete your account"
	msgAlreadyErased   = "this account was erased"
	exportAuditMaxRows = 100000
)

// ---------- export ----------

// exportMyData: GET /api/users/me/export. JSON with the caller's own data. No
// secrets (password hashes, TOTP secrets, recovery codes, key material, tokens)
// and no file contents. Recorded as personal_data_exported before it is served.
func (s *Server) exportMyData(w http.ResponseWriter, r *http.Request) {
	u := s.ctxUser(r)
	if u.IsGuest {
		jerr(w, 403, "guest session is upload-only")
		return
	}
	s.fillMFA(u)
	usage, _ := s.St.UserStorageUsage(u.ID)
	var termsVersion string
	var termsAt sql.NullTime
	s.St.DB.QueryRow(`SELECT terms_accepted_version, terms_accepted_at FROM users WHERE id=?`, u.ID).Scan(&termsVersion, &termsAt)
	out := map[string]any{
		"format":      "sendora-personal-data-1",
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"application": s.St.Setting("application_name"),
		"profile": map[string]any{
			"id": u.ID, "username": u.Username, "name": u.Name, "email": u.Email, "email_verified": u.EmailVerified,
			"created_at": u.CreatedAt, "administrator": u.Admin, "auditor": u.Auditor, "has_password": u.passwordHash != "",
			"must_change_password": u.MustChangePassword,
		},
		"settings": map[string]any{
			"email_language": u.Language, "ui_language": u.UILanguage, "mfa_policy": u.MFAPolicy,
			"quota_bytes": u.QuotaBytes, "quota_inherits_default": u.QuotaInheritsDefault, "storage": usage,
			"terms_accepted_version": termsVersion, "terms_accepted_at": nullTime(termsAt),
		},
	}
	f := s.loadFactors(u.ID)
	mfa := map[string]any{"enabled": u.MFAEnabled, "recovery_codes_remaining": f.Recovery, "security_keys": s.webauthnCredentials(u.ID)}
	var totpCreated sql.NullTime
	if s.St.DB.QueryRow(`SELECT created_at FROM user_totp WHERE user_id=? AND confirmed=1`, u.ID).Scan(&totpCreated) == nil {
		mfa["authenticator_app"] = map[string]any{"set_up": true, "created_at": nullTime(totpCreated)}
	} else {
		mfa["authenticator_app"] = map[string]any{"set_up": false}
	}
	out["mfa"] = mfa
	out["linked_logins"] = s.exportRows(`SELECT p.name, p.type, up.provider_user_id FROM user_auth_providers up JOIN auth_providers p ON p.id=up.provider_id WHERE up.user_id=?`,
		[]string{"provider", "type", "provider_user_id"}, u.ID)
	out["shares"] = s.exportRows(`SELECT s.id, s.name, s.description, s.status, s.public, s.password_hash<>'' , s.download_limit, s.download_count, s.expires_at, s.created_at,
		(SELECT COUNT(*) FROM files f WHERE f.share_id=s.id), (SELECT COALESCE(SUM(size),0) FROM files f WHERE f.share_id=s.id)
		FROM shares s WHERE s.user_id=? ORDER BY s.id`,
		[]string{"id", "name", "description", "status", "public", "password_protected", "download_limit", "download_count", "expires_at", "created_at", "file_count", "total_bytes"}, u.ID)
	out["share_files"] = s.exportRows(`SELECT f.share_id, f.full_path, f.size, f.type FROM files f JOIN shares s ON s.id=f.share_id WHERE s.user_id=? ORDER BY f.share_id, f.id`,
		[]string{"share_id", "path", "size", "type"}, u.ID)
	// Downloads of the caller's shares, as the owner sees them (no recipient addresses).
	out["download_history"] = s.exportRows(`SELECT d.share_id, COALESCE(f.name,''), d.created_at FROM downloads d JOIN shares s ON s.id=d.share_id
		LEFT JOIN files f ON f.id=d.file_id WHERE s.user_id=? ORDER BY d.id`, []string{"share_id", "file", "started_at"}, u.ID)
	out["invites"] = s.exportRows(`SELECT id, recipient_name, recipient_email, message, expires_at, used, completed_at, created_at FROM reverse_invites WHERE user_id=? ORDER BY id`,
		[]string{"id", "recipient_name", "recipient_email", "message", "expires_at", "used", "completed_at", "created_at"}, u.ID)
	out["sessions"] = s.exportRows(`SELECT session_started, issued_at, expires_at FROM refresh_tokens WHERE user_id=? ORDER BY issued_at`,
		[]string{"signed_in_at", "last_refreshed_at", "expires_at"}, u.ID)
	out["audit_records"] = s.exportRows(`SELECT seq, ts, category, actor_role, action, target, outcome, detail, ip FROM audit_log WHERE actor_id=? ORDER BY seq LIMIT `+strconv.Itoa(exportAuditMaxRows),
		[]string{"seq", "time_utc", "category", "role", "action", "target", "outcome", "details", "ip"}, u.ID)
	w.Header().Set("Content-Disposition", `attachment; filename="sendora-my-data-`+time.Now().UTC().Format("20060102")+`.json"`)
	w.Header().Set("Cache-Control", "no-store")
	jout(w, 200, out)
}

func nullTime(t sql.NullTime) any {
	if !t.Valid {
		return nil
	}
	return t.Time.UTC().Format(time.RFC3339)
}

// exportRows runs a query and returns its rows as objects with the given keys.
// Byte values become strings; times are RFC 3339 (UTC).
func (s *Server) exportRows(query string, keys []string, args ...any) []map[string]any {
	out := []map[string]any{}
	rows, err := s.St.DB.Query(query, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		vals := make([]any, len(keys))
		ptrs := make([]any, len(keys))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if rows.Scan(ptrs...) != nil {
			continue
		}
		m := make(map[string]any, len(keys))
		for i, k := range keys {
			switch v := vals[i].(type) {
			case []byte:
				m[k] = string(v)
			case time.Time:
				m[k] = v.UTC().Format(time.RFC3339)
			default:
				m[k] = v
			}
		}
		out = append(out, m)
	}
	return out
}

// ---------- erasure ----------

// refuseErased answers 409 for an account that was erased. It has answered
// when it returns true.
func (s *Server) refuseErased(w http.ResponseWriter, id int64) bool {
	var erased sql.NullTime
	if s.St.DB.QueryRow(`SELECT erased_at FROM users WHERE id=?`, id).Scan(&erased) == nil && erased.Valid {
		jerr(w, 409, msgAlreadyErased)
		return true
	}
	return false
}

// lastAdmin reports whether id is the only remaining administrator.
func (s *Server) lastAdmin(id int64) bool {
	var admin bool
	if s.St.DB.QueryRow(`SELECT admin FROM users WHERE id=?`, id).Scan(&admin) != nil || !admin {
		return false
	}
	var others int
	s.St.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE admin=1 AND is_guest=0 AND erased_at IS NULL AND id<>?`, id).Scan(&others)
	return others == 0
}

// eraseAccount deletes the account's content and pseudonymises the row. It
// writes the error response and returns false on failure.
func (s *Server) eraseAccount(w http.ResponseWriter, id int64) bool {
	unlock := lockStripe(&s.loginLocks, strconv.FormatInt(id, 10))
	defer unlock()
	unlockUploads := s.LockUserUploads(id)
	defer unlockUploads()
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "erasure failed")
		return false
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE users SET id=id WHERE id=? AND is_guest=0 AND erased_at IS NULL`, id)
	if err != nil {
		jerr(w, 500, "erasure failed")
		return false
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		jerr(w, 404, "not found")
		return false
	}
	const sharesOf = `SELECT id FROM shares WHERE user_id=? OR user_id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=?) OR invite_id IN (SELECT id FROM reverse_invites WHERE user_id=?)`
	var dirs []string
	for _, q := range []struct{ sql, kind string }{
		{`SELECT id FROM upload_sessions WHERE user_id=? OR quota_owner_id=? OR user_id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=?)`, "uploads"},
		{`SELECT long_id FROM shares WHERE user_id=? OR user_id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=?) OR invite_id IN (SELECT id FROM reverse_invites WHERE user_id=?)`, "shares"},
	} {
		rows, err := tx.Query(q.sql, id, id, id)
		if err != nil {
			jerr(w, 500, "erasure failed")
			return false
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				jerr(w, 500, "erasure failed")
				return false
			}
			if q.kind == "shares" {
				if dir, derr := store.ShareDir(s.St.DataDir, name); derr == nil {
					dirs = append(dirs, dir)
				}
				continue
			}
			dirs = append(dirs, s.uploadDir(name))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			jerr(w, 500, "erasure failed")
			return false
		}
	}
	for _, q := range []string{
		`DELETE FROM download_mail_outbox WHERE share_id IN (` + sharesOf + `)`,
		`DELETE FROM upload_sessions WHERE user_id=? OR quota_owner_id=? OR user_id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=?)`,
		`DELETE FROM shares WHERE user_id=? OR user_id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=?) OR invite_id IN (SELECT id FROM reverse_invites WHERE user_id=?)`,
		`DELETE FROM users WHERE is_guest=1 AND id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=?)`,
		`DELETE FROM reverse_invites WHERE user_id=?`,
		`DELETE FROM refresh_tokens WHERE user_id=?`,
		`DELETE FROM refresh_spent WHERE user_id=?`,
		`DELETE FROM password_resets WHERE user_id=?`,
		`DELETE FROM email_verifications WHERE user_id=?`,
		`DELETE FROM verification_attempts WHERE user_id=?`,
		`DELETE FROM mfa_reset_requests WHERE user_id=?`,
		`DELETE FROM user_totp WHERE user_id=?`,
		`DELETE FROM user_recovery_codes WHERE user_id=?`,
		`DELETE FROM user_webauthn WHERE user_id=?`,
		`DELETE FROM user_auth_providers WHERE user_id=?`,
		`DELETE FROM user_signin_seen WHERE user_id=?`,
		`UPDATE users SET username='erased-'||id, name='', email='', password_hash='', email_verified=0, must_change_password=0,
			admin=0, is_auditor=0, pending_registration=0, login_failures=0, login_locked_until=0, mfa_epoch=mfa_epoch+1,
			terms_accepted_version='', terms_accepted_at=NULL, erased_at=CURRENT_TIMESTAMP WHERE id=?`,
	} {
		args := make([]any, strings.Count(q, "?"))
		for i := range args {
			args[i] = id
		}
		if _, err := tx.Exec(q, args...); err != nil {
			jerr(w, 500, "erasure failed")
			return false
		}
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "erasure failed")
		return false
	}
	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			jerr(w, 500, "account erased but file cleanup failed")
			return false
		}
	}
	return true
}

// eraseUser: POST /api/users/{id}/erase (administrators; critical audited
// user_erased, needs a step-up).
func (s *Server) eraseUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		jerr(w, 404, "not found")
		return
	}
	me := s.ctxUser(r)
	if id == me.ID {
		jerr(w, 400, msgEraseSelfAdmin)
		return
	}
	target, err := s.userByID(id)
	if err != nil || target.IsGuest {
		jerr(w, 404, "not found")
		return
	}
	if s.refuseErased(w, id) {
		return
	}
	if s.lastAdmin(id) {
		jerr(w, 409, msgEraseLastAdmin)
		return
	}
	if s.refuseLastExternalAdmin(w, id, 0) {
		return
	}
	label := s.userTarget(id) // the former name, before it is replaced
	if !s.eraseAccount(w, id) {
		return
	}
	s.logEvent(me.ID, me.Username, "user_erased", label, "by administrator")
	jout(w, 200, map[string]any{"ok": true})
}

// eraseMe: POST /api/users/me/erase {password}. Accounts with a password
// confirm with it; accounts that sign in only through a provider need a recent
// sign-in. The last administrator cannot erase itself.
func (s *Server) eraseMe(w http.ResponseWriter, r *http.Request) {
	u := s.ctxUser(r)
	if u.IsGuest {
		jerr(w, 403, "guest session is upload-only")
		return
	}
	var b struct {
		Password string `json:"password"`
	}
	if decode(r, &b) != nil {
		jerr(w, 400, "bad request")
		return
	}
	if u.passwordHash != "" {
		if s.stepUpLocked(w, u) {
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(u.passwordHash), []byte(b.Password)) != nil {
			s.stepUpFailed(w, u)
			jerr(w, 400, "current password incorrect")
			return
		}
	} else if time.Since(s.requestAuthTime(r)) > recentSignIn {
		jerr(w, 403, msgEraseRecent)
		return
	}
	if s.lastAdmin(u.ID) {
		jerr(w, 409, msgEraseLastAdmin)
		return
	}
	if s.refuseLastExternalAdmin(w, u.ID, 0) {
		return
	}
	label := s.userTarget(u.ID)
	if !s.eraseAccount(w, u.ID) {
		return
	}
	s.logEvent(u.ID, u.Username, "user_erased", label, "self-service")
	s.clearRefreshCookie(w, r)
	jout(w, 200, map[string]any{"ok": true})
}

// resetTokenIdentity returns the username and email of the account a valid
// password reset token belongs to (nothing for an unknown token), so the new
// password can be compared with them.
func (s *Server) resetTokenIdentity(token string) []string {
	var name, email string
	if s.St.DB.QueryRow(`SELECT u.username, u.email FROM password_resets p JOIN users u ON u.id=p.user_id WHERE p.hash=? AND p.expires_at>?`,
		hashToken(token), time.Now()).Scan(&name, &email) != nil {
		return nil
	}
	return []string{name, email}
}
