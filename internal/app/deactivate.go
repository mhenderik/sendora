package app

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Account deactivation: a reversible alternative to deleting an account. While
// an account is disabled it cannot sign in (password, second factor, provider),
// its sessions are gone and its access tokens are refused, its share links
// answer 410 like expired shares (nothing is deleted), and its pending upload
// sessions and invitations are revoked. Deleting stays a separate action.

const (
	msgAccountDisabled      = "account disabled"
	msgDisableSelf          = "you cannot disable your own account"
	msgDisableLastAdmin     = "cannot disable the last active administrator"
	msgAlreadyDisabled      = "account already disabled"
	msgNotDisabled          = "account is not disabled"
	msgDisableReasonInvalid = "the reason must be at most 200 characters without control characters"
	disableReasonMax        = 200
)

// refuseDisabledSignIn answers 403 for a sign-in by a disabled account and
// records the attempt. The current state is read from the database, so an
// account disabled during a sign-in (between password and second factor) is
// refused as well.
func (s *Server) refuseDisabledSignIn(w http.ResponseWriter, u *User, method string) bool {
	disabled := u.Disabled
	var stored bool
	if err := s.St.DB.QueryRow(`SELECT disabled_at IS NOT NULL FROM users WHERE id=?`, u.ID).Scan(&stored); err == nil {
		disabled = disabled || stored
	}
	if !disabled {
		return false
	}
	detail := msgAccountDisabled
	if method != "" {
		detail += "; " + method
	}
	s.logEvent(u.ID, u.Username, "login_failed", "", detail)
	jerr(w, http.StatusForbidden, msgAccountDisabled)
	return true
}

// shareOwnerDisabled reports whether the owner of a share is disabled. Files
// received through an invitation belong to the inviter, so a disabled inviter
// counts too.
func (s *Server) shareOwnerDisabled(shareID int64) bool {
	var n int
	err := s.St.DB.QueryRow(`SELECT COUNT(*) FROM shares s
		LEFT JOIN users u ON u.id=s.user_id
		LEFT JOIN reverse_invites ri ON ri.id=s.invite_id
		LEFT JOIN users iu ON iu.id=ri.user_id
		WHERE s.id=? AND (u.disabled_at IS NOT NULL OR iu.disabled_at IS NOT NULL)`, shareID).Scan(&n)
	return err == nil && n > 0
}

// noteCreatedBy records who created an account ("setup", "self-registration",
// "provider <name>" or the administrator's label).
func (s *Server) noteCreatedBy(id int64, by string) {
	s.St.DB.Exec(`UPDATE users SET created_by=? WHERE id=?`, clip(by, 160), id)
}

// noteRoleChange and noteEmailChange keep the last change of role and email
// address on the account, so the access review does not depend on the audit
// log's retention.
func (s *Server) noteRoleChange(id int64, actor *User) {
	s.St.DB.Exec(`UPDATE users SET role_changed_at=?,role_changed_by=? WHERE id=?`, time.Now().UTC(), s.userTarget(actor.ID), id)
}

func (s *Server) noteEmailChange(id int64, actor *User) {
	s.St.DB.Exec(`UPDATE users SET email_changed_at=?,email_changed_by=? WHERE id=?`, time.Now().UTC(), s.userTarget(actor.ID), id)
}

// activeAdminsExcept counts the administrators that are not disabled, without
// the given account.
func (s *Server) activeAdminsExcept(id int64) int {
	var n int
	if err := s.St.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE admin=1 AND is_guest=0 AND disabled_at IS NULL AND id<>?`, id).Scan(&n); err != nil {
		return 0
	}
	return n
}

// disableUser: POST /api/users/{id}/disable {"reason": "..."} (administrators).
func (s *Server) disableUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jerr(w, 404, "not found")
		return
	}
	me := s.ctxUser(r)
	if id == me.ID {
		jerr(w, 400, msgDisableSelf)
		return
	}
	var b struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &b); err != nil && !errors.Is(err, io.EOF) {
		jerr(w, 400, "bad request")
		return
	}
	reason := strings.TrimSpace(b.Reason)
	if !plainText(reason, disableReasonMax) {
		jerr(w, 400, msgDisableReasonInvalid)
		return
	}
	unlock := lockStripe(&s.loginLocks, strconv.FormatInt(id, 10))
	defer unlock()
	target, err := s.userByID(id)
	if err != nil || target.IsGuest {
		jerr(w, 404, "not found")
		return
	}
	if target.Disabled {
		jerr(w, 409, msgAlreadyDisabled)
		return
	}
	if target.Admin && s.activeAdminsExcept(id) == 0 {
		jerr(w, 409, msgDisableLastAdmin)
		return
	}
	if target.Admin && s.refuseLastExternalAdmin(w, id, 0) {
		return
	}
	by := s.userTarget(me.ID) // before the transaction takes the only connection
	unlockUploads := s.LockUserUploads(id)
	defer unlockUploads()
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	res, err := tx.Exec(`UPDATE users SET disabled_at=?,disabled_by=?,disabled_reason=? WHERE id=? AND is_guest=0 AND disabled_at IS NULL`, now, by, reason, id)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		jerr(w, 409, msgAlreadyDisabled)
		return
	}
	// Pending uploads of the account and of the guests it invited.
	const uploadsOf = `user_id=? OR quota_owner_id=? OR user_id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=? AND guest_user_id IS NOT NULL)`
	rows, err := tx.Query(`SELECT id FROM upload_sessions WHERE `+uploadsOf, id, id, id)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	var uploads []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			jerr(w, 500, "database error")
			return
		}
		uploads = append(uploads, sid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	var invites int64
	for _, q := range []struct {
		sql   string
		args  []any
		count *int64
	}{
		{`DELETE FROM upload_sessions WHERE ` + uploadsOf, []any{id, id, id}, nil},
		// Open invitations (and their unused children) stop working now.
		{`UPDATE reverse_invites SET expires_at=? WHERE user_id=? AND completed_at IS NULL AND expires_at>?`, []any{now, id, now}, &invites},
		// Every session of the account and of its guests ends.
		{`DELETE FROM refresh_tokens WHERE user_id=? OR user_id IN (SELECT guest_user_id FROM reverse_invites WHERE user_id=? AND guest_user_id IS NOT NULL)`, []any{id, id}, nil},
		{`DELETE FROM password_resets WHERE user_id=?`, []any{id}, nil},
		{`DELETE FROM email_verifications WHERE user_id=?`, []any{id}, nil},
		{`DELETE FROM mfa_reset_requests WHERE user_id=?`, []any{id}, nil},
	} {
		res, err := tx.Exec(q.sql, q.args...)
		if err != nil {
			jerr(w, 500, "database error")
			return
		}
		if q.count != nil {
			*q.count, _ = res.RowsAffected()
		}
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "database error")
		return
	}
	cleanupFailed := false
	for _, sid := range uploads {
		if err := os.RemoveAll(s.uploadDir(sid)); err != nil {
			log.Printf("disable user %d: upload cleanup: %v", id, err)
			cleanupFailed = true
		}
	}
	detail := "uploads revoked: " + strconv.Itoa(len(uploads)) + ", invites revoked: " + strconv.FormatInt(invites, 10)
	if reason != "" {
		detail += ", reason: " + reason
	}
	s.logEvent(me.ID, me.Username, "user_disabled", s.userTarget(id), detail)
	out := map[string]any{"ok": true, "uploads_revoked": len(uploads), "invites_revoked": invites}
	if cleanupFailed {
		out["cleanup_failed"] = true
	}
	jout(w, 200, out)
}

// enableUser: POST /api/users/{id}/enable (administrators). Revoked invitations
// and uploads stay revoked; share links work again (unless expired meanwhile).
func (s *Server) enableUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jerr(w, 404, "not found")
		return
	}
	unlock := lockStripe(&s.loginLocks, strconv.FormatInt(id, 10))
	defer unlock()
	target, err := s.userByID(id)
	if err != nil || target.IsGuest {
		jerr(w, 404, "not found")
		return
	}
	res, err := s.St.DB.Exec(`UPDATE users SET disabled_at=NULL,disabled_by='',disabled_reason='' WHERE id=? AND is_guest=0 AND disabled_at IS NOT NULL`, id)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		jerr(w, 409, msgNotDisabled)
		return
	}
	me := s.ctxUser(r)
	s.logEvent(me.ID, me.Username, "user_enabled", s.userTarget(id), "")
	jout(w, 200, map[string]any{"ok": true})
}
