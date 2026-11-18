package app

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Administrators must not be able to take over another user's account silently:
// that would give them access to the user's shares. Everything that changes a
// user's credentials therefore goes through a link that only reaches the user's
// verified mailbox, and the user is told about it. Tokens are never returned in
// a response and never logged.

const (
	msgAdminNoPassword = "an administrator cannot set another user's password; use 'Send password reset link' instead"
	msgMFANoVerified   = "this user has no verified email address, so they cannot confirm the reset; stop the application and use the offline tool 'sendora-db mfa-reset' instead"
	msgMFANoSMTP       = "outgoing email (SMTP) is not configured, so the user cannot confirm the reset; configure it, or stop the application and use the offline tool 'sendora-db mfa-reset'"

	passwordResetTTL = time.Hour
	mfaResetTTL      = 24 * time.Hour
)

func recipientName(u *User) string {
	if n := strings.TrimSpace(u.Name); n != "" {
		return n
	}
	return u.Username
}

// notifyEmailChanged tells the user, at the address they had before, that an
// administrator changed it. Nothing is sent when that address was unverified.
func (s *Server) notifyEmailChanged(previous *User) {
	if previous == nil || previous.IsGuest || !previous.EmailVerified || strings.TrimSpace(previous.Email) == "" || s.smtpConfigured() != nil {
		return
	}
	s.queueMail(previous.Email, "", "tpl_account_email_changed_notice", map[string]any{
		"RecipientName": recipientName(previous),
		"Username":      previous.Username,
		"EmailLanguage": previous.Language,
	})
}

// sendAdminPasswordReset mails the user a one-time link for choosing a new
// password, only to a verified address. It returns an outcome and, when nothing
// was sent, a short admin-facing reason.
func (s *Server) sendAdminPasswordReset(r *http.Request, actor *User, id int64) (string, string) {
	u, err := s.userByID(id)
	switch {
	case err != nil || u.IsGuest:
		return welcomeSkipped, "this account cannot receive a reset link"
	case u.passwordHash == "" || !s.localLoginEnabled():
		return welcomeSkipped, "this user has no local password to reset"
	case strings.TrimSpace(u.Email) == "" || !u.EmailVerified:
		return welcomeSkipped, "this user has no verified email address; they will be asked to change their password at next sign-in"
	case s.smtpConfigured() != nil:
		return welcomeSkipped, "outgoing email (SMTP) is not configured"
	}
	base, ok := s.emailBaseURL(r)
	if !ok {
		return welcomeSkipped, "set the public application URL (APP_URL) first, so the links in the email are trustworthy"
	}
	if !s.takeMail(actor.ID, 1) {
		return welcomeSkipped, "the hourly limit for outgoing emails was reached; try again later"
	}
	tok := randHex(24)
	hash := hashToken(tok)
	tx, err := s.St.DB.Begin()
	if err != nil {
		return welcomeFailed, "the reset email could not be prepared"
	}
	// A fresh link replaces older unused reset links of this user.
	if _, err := tx.Exec(`DELETE FROM password_resets WHERE user_id=? AND kind='reset'`, u.ID); err != nil {
		tx.Rollback()
		return welcomeFailed, "the reset email could not be prepared"
	}
	if _, err := tx.Exec(`INSERT INTO password_resets(hash,user_id,expires_at,kind) VALUES(?,?,?,'reset')`, hash, u.ID, time.Now().Add(passwordResetTTL)); err != nil {
		tx.Rollback()
		return welcomeFailed, "the reset email could not be prepared"
	}
	if err := tx.Commit(); err != nil {
		return welcomeFailed, "the reset email could not be prepared"
	}
	if err := s.deliverTemplate(u.Email, "tpl_admin_password_reset", map[string]any{
		"RecipientName": recipientName(u),
		"URL":           base + "/reset-password/" + tok,
		"EmailLanguage": u.Language,
	}); err != nil {
		s.St.DB.Exec(`DELETE FROM password_resets WHERE hash=?`, hash)
		return welcomeFailed, "the email could not be delivered; check the SMTP settings"
	}
	return welcomeSent, ""
}

// mfaReset: POST /api/users/{id}/mfa/reset (admin). It only asks the user to
// confirm by email; the factors are removed by mfaResetConfirm.
func (s *Server) mfaReset(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		jerr(w, 404, "not found")
		return
	}
	u, err := s.userByID(id)
	if err != nil || u.IsGuest {
		jerr(w, 404, "not found")
		return
	}
	me := s.ctxUser(r)
	switch {
	case strings.TrimSpace(u.Email) == "" || !u.EmailVerified:
		jerr(w, 409, msgMFANoVerified)
		return
	case s.smtpConfigured() != nil:
		jerr(w, 409, msgMFANoSMTP)
		return
	}
	base, ok := s.emailBaseURL(r)
	if !ok {
		jerr(w, 409, "set the public application URL (APP_URL) first, so the links in the email are trustworthy")
		return
	}
	if !s.takeMail(me.ID, 1) {
		w.Header().Set("Retry-After", "600")
		jerr(w, 429, "the hourly limit for outgoing emails was reached; try again later")
		return
	}
	tok := randHex(24)
	hash := hashToken(tok)
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	// Only the newest request stays valid.
	if _, err := tx.Exec(`DELETE FROM mfa_reset_requests WHERE user_id=?`, id); err != nil {
		jerr(w, 500, "reset failed")
		return
	}
	if _, err := tx.Exec(`INSERT INTO mfa_reset_requests(hash,user_id,requested_by,expires_at) VALUES(?,?,?,?)`, hash, id, me.ID, time.Now().Add(mfaResetTTL)); err != nil {
		jerr(w, 500, "reset failed")
		return
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "reset failed")
		return
	}
	if err := s.deliverTemplate(u.Email, "tpl_mfa_reset_requested", map[string]any{
		"RecipientName": recipientName(u),
		"URL":           base + "/mfa-reset/" + tok,
		"EmailLanguage": u.Language,
	}); err != nil {
		s.St.DB.Exec(`DELETE FROM mfa_reset_requests WHERE hash=?`, hash)
		jerr(w, 502, "the email could not be delivered; check the SMTP settings")
		return
	}
	s.logEvent(me.ID, me.Username, "mfa_reset_requested", s.userTarget(id), "")
	jout(w, 200, map[string]any{"ok": true, "pending": true})
}

// mfaResetConfirm: POST /api/auth/mfa-reset/confirm {token} (public). The link
// only reaches the user's verified mailbox; it works once.
func (s *Server) mfaResetConfirm(w http.ResponseWriter, r *http.Request) {
	var b struct{ Token string }
	if decode(r, &b) != nil || b.Token == "" || len(b.Token) > 200 {
		jerr(w, 400, "invalid or expired token")
		return
	}
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	var uid, requestedBy int64
	// The conditional update is the single-use gate and takes the write lock.
	if err := tx.QueryRow(`UPDATE mfa_reset_requests SET used=1 WHERE hash=? AND used=0 AND expires_at>?
		AND user_id IN (SELECT id FROM users WHERE is_guest=0 AND email_verified=1 AND email<>'')
		RETURNING user_id, requested_by`, hashToken(b.Token), time.Now()).Scan(&uid, &requestedBy); err != nil {
		jerr(w, 400, "invalid or expired token")
		return
	}
	for _, q := range []string{
		`DELETE FROM user_totp WHERE user_id=?`,
		`DELETE FROM user_recovery_codes WHERE user_id=?`,
		`DELETE FROM user_webauthn WHERE user_id=?`,
		`DELETE FROM refresh_tokens WHERE user_id=?`,
		`DELETE FROM mfa_reset_requests WHERE user_id=?`,
		`UPDATE users SET mfa_epoch=mfa_epoch+1 WHERE id=?`,
	} {
		if _, err := tx.Exec(q, uid); err != nil {
			jerr(w, 500, "reset failed")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "reset failed")
		return
	}
	if u, err := s.userByID(uid); err == nil {
		s.logEvent(u.ID, u.Username, "mfa_reset_confirmed", s.userTarget(uid), "requested_by="+strconv.FormatInt(requestedBy, 10))
		if s.smtpConfigured() == nil {
			s.queueMail(u.Email, "", "tpl_mfa_reset_done", map[string]any{
				"RecipientName": recipientName(u),
				"EmailLanguage": u.Language,
			})
		}
	}
	jout(w, 200, map[string]any{"ok": true})
}
