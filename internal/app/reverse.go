package app

import (
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) {
	u := s.ctxUser(r)
	if u.IsGuest || !s.St.SettingBool("allow_reverse_shares") {
		jerr(w, 403, "reverse shares disabled")
		return
	}
	var b struct {
		Name          string `json:"name"`
		Email         string `json:"email"`
		Message       string `json:"message"`
		Days          int    `json:"days"`
		EmailLanguage string `json:"email_language"`
		Open          bool   `json:"open"`
		MaxUploads    int    `json:"max_uploads"`
	}
	if decode(r, &b) != nil {
		jerr(w, 400, "bad request")
		return
	}
	if b.EmailLanguage == "" {
		b.EmailLanguage = "en"
	}
	if !s.St.ValidMailLanguage(b.EmailLanguage) {
		jerr(w, 400, "unsupported email language")
		return
	}
	if b.Email != "" {
		if _, err := mail.ParseAddress(b.Email); err != nil {
			jerr(w, 400, "invalid email")
			return
		}
	}
	if validateText(b.Name, maxShareNameRunes, true) != nil || validateText(b.Message, maxMessageRunes, false) != nil {
		jerr(w, 400, "name or message too long or contains invalid characters")
		return
	}
	if b.Days <= 0 || b.Days > 30 {
		b.Days = 7
	}
	if b.Open {
		if b.Email != "" {
			jerr(w, 400, "open links do not take an email address")
			return
		}
		if b.MaxUploads < 1 || b.MaxUploads > 100 {
			jerr(w, 400, "max_uploads must be between 1 and 100")
			return
		}
	} else {
		b.MaxUploads = 0
	}
	if b.Email != "" && !s.takeMail(u.ID, 1) {
		w.Header().Set("Retry-After", "3600")
		jerr(w, 429, "too many emails sent recently; try again later")
		return
	}
	tok := randHex(24)
	exp := time.Now().AddDate(0, 0, b.Days)
	res, err := s.St.DB.Exec(`INSERT INTO reverse_invites(token,user_id,recipient_name,recipient_email,message,expires_at,reusable,max_uploads) VALUES(?,?,?,?,?,?,?,?)`,
		tok, u.ID, b.Name, b.Email, b.Message, exp, b.Open, b.MaxUploads)
	if err != nil {
		jerr(w, 500, "invite unavailable")
		return
	}
	inviteID, _ := res.LastInsertId()
	kind := "email"
	if b.Open {
		kind = "open"
	}
	s.logEvent(u.ID, u.Username, "invite_created", "invite#"+strconv.FormatInt(inviteID, 10), kind)
	link := s.baseURL(r) + "/invite/" + tok
	if b.Open {
		id, _ := res.LastInsertId()
		jout(w, 201, map[string]any{"id": id, "kind": "open", "url": link, "expires_at": exp, "max_uploads": b.MaxUploads, "used": 0})
		return
	}
	if base, ok := s.emailBaseURL(r); b.Email != "" && ok {
		s.queueMail(b.Email, nameOf(u)+" invites you to upload files", "tpl_reverse_invite", map[string]any{
			"EmailLanguage": b.EmailLanguage, "RecipientName": b.Name, "SenderName": nameOf(u), "URL": base + "/invite/" + tok, "Message": b.Message})
	}
	jout(w, 201, map[string]any{"kind": "email", "url": link, "expires_at": exp})
}

// listInvites shows the caller's own invites. Child rows (one per accepted open
// link) are internal and folded into the parent's used count. The URL is only
// ever returned for open links, to their owner.
func (s *Server) listInvites(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	base := s.baseURL(r) // may query settings: must run before rows hold the sole connection
	rows, err := s.St.DB.Query(`SELECT i.id,i.token,i.recipient_name,i.recipient_email,i.expires_at,i.completed_at,i.reusable,i.max_uploads,
		(SELECT COUNT(*) FROM reverse_invites c WHERE c.parent_id=i.id AND c.completed_at IS NOT NULL),
		(SELECT COUNT(*) FROM reverse_invites c WHERE c.parent_id=i.id AND c.completed_at IS NULL AND c.expires_at>?)
		FROM reverse_invites i WHERE i.user_id=? AND i.parent_id IS NULL ORDER BY i.created_at DESC, i.id DESC`, now, s.ctxUser(r).ID)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var tok, n, e string
		var exp time.Time
		var done *time.Time
		var open bool
		var max, used, pending int
		if rows.Scan(&id, &tok, &n, &e, &exp, &done, &open, &max, &used, &pending) != nil {
			continue
		}
		it := map[string]any{"id": id, "name": n, "email": e, "expires_at": exp, "completed_at": done, "kind": "email"}
		if open {
			it["kind"] = "open"
			it["max_uploads"] = max
			it["used"] = used
			it["pending"] = pending
			it["active"] = now.Before(exp)
			it["url"] = base + "/invite/" + tok
		} else {
			it["used"] = 0
			if done != nil {
				it["used"] = 1
			}
			it["max_uploads"] = 1
			it["active"] = done == nil && now.Before(exp)
		}
		out = append(out, it)
	}
	rows.Close()
	jout(w, 200, out)
}

// revokeInvite expires one of the caller's invites now, and for open links also
// every outstanding (incomplete) child. Completed uploads are untouched.
func (s *Server) revokeInvite(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jerr(w, 404, "invite not found")
		return
	}
	u := s.ctxUser(r)
	now := time.Now()
	res, err := s.St.DB.Exec(`UPDATE reverse_invites SET expires_at=? WHERE id=? AND user_id=? AND parent_id IS NULL`, now, id, u.ID)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		jerr(w, 404, "invite not found")
		return
	}
	if _, err := s.St.DB.Exec(`UPDATE reverse_invites SET expires_at=? WHERE parent_id=? AND user_id=? AND completed_at IS NULL AND expires_at>?`, now, id, u.ID, now); err != nil {
		jerr(w, 500, "database error")
		return
	}
	s.logEvent(u.ID, u.Username, "invite_revoked", "invite#"+strconv.FormatInt(id, 10), "")
	jout(w, 200, map[string]any{"revoked": true})
}

// acceptInvite turns an invite token into a guest session limited to one upload.
func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		jerr(w, 403, "cross-origin request refused")
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if decode(r, &in) != nil || in.Token == "" || len(in.Token) > 256 {
		jerr(w, 404, "invite not found")
		return
	}
	tok := in.Token
	unlock := lockStripe(&s.identityLocks, "invite:"+tok)
	defer unlock()
	// Serialize owner deletion with invitation validation and guest creation.
	// No DB helpers may run while this transaction owns the sole connection.
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "invite unavailable")
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE reverse_invites SET used=used WHERE token=?`, tok); err != nil {
		jerr(w, 500, "invite unavailable")
		return
	}
	var id int64
	var guest *int64
	var exp time.Time
	var done *time.Time
	var name, msg, inviter string
	var reusable bool
	var maxUploads int
	var ownerID int64
	var parent *int64
	err = tx.QueryRow(`SELECT i.id,i.guest_user_id,i.expires_at,i.completed_at,i.recipient_name,i.message,COALESCE(NULLIF(u.name,''),u.username),i.reusable,i.max_uploads,i.user_id,i.parent_id FROM reverse_invites i JOIN users u ON u.id=i.user_id WHERE i.token=? AND u.is_guest=0`, tok).
		Scan(&id, &guest, &exp, &done, &name, &msg, &inviter, &reusable, &maxUploads, &ownerID, &parent)
	now := time.Now()
	if err != nil || !now.Before(exp) || done != nil {
		jerr(w, 404, "invite invalid, expired or already used")
		return
	}
	var gu *User
	if reusable && parent == nil {
		// Open link: the link itself never uploads. Each accept mints a fresh
		// guest plus a single-use child invite with its own unreturned token.
		var taken int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM reverse_invites WHERE parent_id=? AND (completed_at IS NOT NULL OR expires_at>?)`, id, now).Scan(&taken); err != nil {
			jerr(w, 500, "invite unavailable")
			return
		}
		if taken >= maxUploads {
			jerr(w, 410, "invite upload limit reached")
			return
		}
		childExp := now.Add(24 * time.Hour)
		if exp.Before(childExp) {
			childExp = exp
		}
		display := strings.TrimSpace(name)
		if display == "" {
			display = "Guest"
		}
		res, err := tx.Exec(`INSERT INTO users(username,name,is_guest) VALUES(?,?,1)`, "guest-"+randHex(6), display)
		var gid int64
		if err == nil {
			gid, err = res.LastInsertId()
		}
		if err == nil {
			_, err = tx.Exec(`INSERT INTO reverse_invites(token,user_id,recipient_name,message,guest_user_id,expires_at,parent_id) VALUES(?,?,?,?,?,?,?)`,
				randHex(24), ownerID, name, msg, gid, childExp, id)
		}
		if err == nil {
			gu, err = scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, gid))
		}
		if err != nil {
			jerr(w, 500, "invite unavailable")
			return
		}
	} else if guest != nil {
		gu, err = scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users WHERE id=? AND is_guest=1`, *guest))
		if err != nil {
			jerr(w, 404, "invite invalid, expired or already used")
			return
		}
	} else {
		uname := "guest-" + randHex(6)
		display := strings.TrimSpace(name)
		if display == "" {
			display = "Guest"
		}
		res, err := tx.Exec(`INSERT INTO users(username,name,is_guest) VALUES(?,?,1)`, uname, display)
		if err != nil {
			jerr(w, 500, "invite unavailable")
			return
		}
		gid, err := res.LastInsertId()
		if err == nil {
			_, err = tx.Exec(`UPDATE reverse_invites SET guest_user_id=? WHERE id=?`, gid, id)
		}
		if err == nil {
			gu, err = scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, gid))
		}
		if err != nil {
			jerr(w, 500, "invite unavailable")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "invite unavailable")
		return
	}
	s.logEvent(gu.ID, gu.Username, "invite_accepted", "invite#"+strconv.FormatInt(id, 10), "")
	s.issueSession(w, r, gu)
	jout(w, 200, map[string]any{"access_token": s.accessToken(gu), "user": gu, "inviter": inviter, "message": msg})
}
