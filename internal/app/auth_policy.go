package app

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mhenderik/sendora/internal/store"
)

const defaultUserQuotaBytes int64 = store.DefaultQuotaBytes
const maxSafeQuotaBytes int64 = store.MaxSafeQuotaBytes

// Nil fields (including JSON null) preserve an update. Explicit quota always
// selects an override; true without quota resets inheritance.
func quotaChoice(quota *int64, inherits *bool) (*bool, bool) {
	if !validQuotaBytes(quota) || (inherits != nil && ((*inherits && quota != nil) || (!*inherits && quota == nil))) {
		return nil, false
	}
	if quota != nil {
		override := false
		return &override, true
	}
	return inherits, true
}

func validQuotaBytes(q *int64) bool {
	return q == nil || (*q >= 0 && *q <= maxSafeQuotaBytes)
}

func rejectLogin(w http.ResponseWriter) {
	time.Sleep(300 * time.Millisecond)
	jerr(w, http.StatusUnauthorized, "invalid credentials")
}

func (s *Server) unlockLogin(w http.ResponseWriter, r *http.Request) {
	if u := s.ctxUser(r); u == nil || !u.Admin {
		jerr(w, http.StatusForbidden, "forbidden")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		jerr(w, http.StatusBadRequest, "invalid user")
		return
	}
	unlock := lockStripe(&s.loginLocks, strconv.FormatInt(id, 10))
	defer unlock()
	res, err := s.St.DB.Exec(`UPDATE users SET login_failures=0,login_locked_until=0 WHERE id=? AND is_guest=0`, id)
	if err != nil {
		jerr(w, http.StatusInternalServerError, "unlock failed")
		return
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		jerr(w, http.StatusNotFound, "not found")
		return
	}
	me := s.ctxUser(r)
	s.logEvent(me.ID, me.Username, "user_unlocked", s.userTarget(id), "")
	jout(w, http.StatusOK, map[string]any{"ok": true})
}
