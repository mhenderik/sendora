package platform

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mhenderik/sendora/internal/store"
)

// Admin is a platform administrator. Platform administrators are separate
// from every tenant's users: they sign in only on the console host and have no
// access to tenant data.
type Admin struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	TOTPEnabled  bool   `json:"totp_enabled"`
	CreatedAt    string `json:"created_at"`
	PasswordHash string `json:"-"`
	TOTPSecret   string `json:"-"`
	PendingTOTP  string `json:"-"`
	LastCounter  int64  `json:"-"`
	Failures     int    `json:"-"`
	LockedUntil  int64  `json:"-"`
}

const adminCols = `id,username,email,totp_enabled,created_at,password_hash,totp_secret,pending_totp_secret,totp_last_counter,failures,locked_until`

func scanAdmin(row interface{ Scan(...any) error }) (*Admin, error) {
	a := &Admin{}
	err := row.Scan(&a.ID, &a.Username, &a.Email, &a.TOTPEnabled, &a.CreatedAt, &a.PasswordHash, &a.TOTPSecret, &a.PendingTOTP, &a.LastCounter, &a.Failures, &a.LockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// AdminCount is the number of platform administrators.
func (p *Platform) AdminCount() int {
	var n int
	p.St.DB.QueryRow(`SELECT COUNT(*) FROM platform_admins`).Scan(&n)
	return n
}

// AdminByName looks an administrator up by username (case-insensitive).
func (p *Platform) AdminByName(name string) (*Admin, error) {
	return scanAdmin(p.St.DB.QueryRow(`SELECT `+adminCols+` FROM platform_admins WHERE username=?`, strings.TrimSpace(name)))
}

// AdminByID looks an administrator up by ID.
func (p *Platform) AdminByID(id int64) (*Admin, error) {
	return scanAdmin(p.St.DB.QueryRow(`SELECT `+adminCols+` FROM platform_admins WHERE id=?`, id))
}

// Admins lists all administrators.
func (p *Platform) Admins() ([]*Admin, error) {
	rows, err := p.St.DB.Query(`SELECT ` + adminCols + ` FROM platform_admins ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Admin{}
	for rows.Next() {
		a, err := scanAdmin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ErrAdminExists is returned for a duplicate username.
var ErrAdminExists = errors.New("this username is already in use")

// CreateAdmin inserts an administrator with a bcrypt hash computed by the caller.
func (p *Platform) CreateAdmin(username, email, hash string) (*Admin, error) {
	res, err := p.St.DB.Exec(`INSERT INTO platform_admins(username,email,password_hash,created_at) VALUES(?,?,?,?)`, username, email, hash, now())
	if err != nil {
		return nil, ErrAdminExists
	}
	id, _ := res.LastInsertId()
	return p.AdminByID(id)
}

// CreateFirstAdmin inserts the first administrator only while there is none
// (atomic, so two concurrent setup requests cannot both succeed).
func (p *Platform) CreateFirstAdmin(username, email, hash string) (*Admin, error) {
	res, err := p.St.DB.Exec(`INSERT INTO platform_admins(username,email,password_hash,created_at) SELECT ?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM platform_admins)`, username, email, hash, now())
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errors.New("already set up")
	}
	id, _ := res.LastInsertId()
	return p.AdminByID(id)
}

// DeleteAdmin removes an administrator (and their sessions); the last one
// cannot be removed.
func (p *Platform) DeleteAdmin(id int64) error {
	res, err := p.St.DB.Exec(`DELETE FROM platform_admins WHERE id=? AND (SELECT COUNT(*) FROM platform_admins)>1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("the last platform administrator cannot be removed")
	}
	return nil
}

// SetPassword replaces an administrator's password hash and ends their sessions.
func (p *Platform) SetPassword(id int64, hash string) error {
	if _, err := p.St.DB.Exec(`UPDATE platform_admins SET password_hash=? WHERE id=?`, hash, id); err != nil {
		return err
	}
	_, err := p.St.DB.Exec(`DELETE FROM platform_sessions WHERE admin_id=?`, id)
	return err
}

// NoteLoginFailure counts a failed sign-in and locks the account for a while
// after repeated failures.
func (p *Platform) NoteLoginFailure(id int64, limit int, lock time.Duration) {
	p.St.DB.Exec(`UPDATE platform_admins SET failures=failures+1, locked_until=CASE WHEN failures+1>=? THEN ? ELSE locked_until END WHERE id=?`, limit, time.Now().Add(lock).Unix(), id)
}

// NoteLoginSuccess clears the failure counter.
func (p *Platform) NoteLoginSuccess(id int64) {
	p.St.DB.Exec(`UPDATE platform_admins SET failures=0, locked_until=0 WHERE id=?`, id)
}

// SetPendingTOTP stores a TOTP secret awaiting confirmation.
func (p *Platform) SetPendingTOTP(id int64, secret string) error {
	_, err := p.St.DB.Exec(`UPDATE platform_admins SET pending_totp_secret=? WHERE id=?`, secret, id)
	return err
}

// EnableTOTP promotes the pending secret after a correct code.
func (p *Platform) EnableTOTP(id int64, counter int64) error {
	_, err := p.St.DB.Exec(`UPDATE platform_admins SET totp_secret=pending_totp_secret, pending_totp_secret='', totp_enabled=1, totp_last_counter=? WHERE id=? AND pending_totp_secret<>''`, counter, id)
	return err
}

// UseTOTPCounter records a used TOTP time step; it fails for a step that is
// not newer than the last one used (no replay of a code).
func (p *Platform) UseTOTPCounter(id, counter int64) bool {
	res, err := p.St.DB.Exec(`UPDATE platform_admins SET totp_last_counter=? WHERE id=? AND totp_last_counter<?`, counter, id, counter)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// ResetTOTP removes an administrator's TOTP (another administrator's action);
// they must enroll again at the next sign-in.
func (p *Platform) ResetTOTP(id int64) error {
	if _, err := p.St.DB.Exec(`UPDATE platform_admins SET totp_secret='', pending_totp_secret='', totp_enabled=0, totp_last_counter=0 WHERE id=?`, id); err != nil {
		return err
	}
	_, err := p.St.DB.Exec(`DELETE FROM platform_sessions WHERE admin_id=?`, id)
	return err
}

// ---------- sessions ----------

// Session is a console session (opaque random token in a host-only cookie).
type Session struct {
	AdminID   int64
	Stage     string // mfa (password ok, TOTP due), enroll (must set up TOTP), full
	CreatedAt int64
	MFAAt     int64
}

// CreateSession stores a session by the hash of its token.
func (p *Platform) CreateSession(hash string, adminID int64, stage string, ttl time.Duration) error {
	n := time.Now().Unix()
	_, err := p.St.DB.Exec(`INSERT INTO platform_sessions(hash,admin_id,stage,created_at,last_seen,expires_at,mfa_at) VALUES(?,?,?,?,?,?,?)`,
		hash, adminID, stage, n, n, time.Now().Add(ttl).Unix(), map[bool]int64{true: n, false: 0}[stage == "full"])
	return err
}

// SessionFor returns a live session and slides its idle timeout.
func (p *Platform) SessionFor(hash string, idle time.Duration) (*Session, error) {
	s := &Session{}
	var lastSeen, expires int64
	err := p.St.DB.QueryRow(`SELECT admin_id,stage,created_at,last_seen,expires_at,mfa_at FROM platform_sessions WHERE hash=?`, hash).Scan(&s.AdminID, &s.Stage, &s.CreatedAt, &lastSeen, &expires, &s.MFAAt)
	if err != nil {
		return nil, ErrNotFound
	}
	n := time.Now()
	if n.Unix() > expires || n.Sub(time.Unix(lastSeen, 0)) > idle {
		p.St.DB.Exec(`DELETE FROM platform_sessions WHERE hash=?`, hash)
		return nil, ErrNotFound
	}
	if n.Unix()-lastSeen > 30 {
		p.St.DB.Exec(`UPDATE platform_sessions SET last_seen=? WHERE hash=?`, n.Unix(), hash)
	}
	return s, nil
}

// PromoteSession moves a session to the full stage after MFA.
func (p *Platform) PromoteSession(hash string) error {
	_, err := p.St.DB.Exec(`UPDATE platform_sessions SET stage='full', mfa_at=? WHERE hash=?`, time.Now().Unix(), hash)
	return err
}

// TouchMFA records a fresh second-factor confirmation (step-up).
func (p *Platform) TouchMFA(hash string) {
	p.St.DB.Exec(`UPDATE platform_sessions SET mfa_at=? WHERE hash=?`, time.Now().Unix(), hash)
}

// DeleteSession ends one session.
func (p *Platform) DeleteSession(hash string) {
	p.St.DB.Exec(`DELETE FROM platform_sessions WHERE hash=?`, hash)
}

// PruneSessions removes expired sessions.
func (p *Platform) PruneSessions() {
	p.St.DB.Exec(`DELETE FROM platform_sessions WHERE expires_at<?`, time.Now().Unix())
}

// ---------- audit ----------

// Audit appends one record to the platform audit log (tamper-evident chain,
// same format and verification as a tenant's audit log).
func (p *Platform) Audit(actorID int64, actorName, action, target, outcome, detail, ip string) error {
	if outcome == "" {
		outcome = "ok"
	}
	cat := "platform"
	switch {
	case strings.HasPrefix(action, "login") || strings.HasPrefix(action, "logout") || strings.HasPrefix(action, "mfa_"):
		cat = "auth"
	case strings.HasPrefix(action, "system_"):
		cat = "system"
	}
	role := "platform_admin"
	if actorID == 0 {
		role = "system"
		if actorName == "" {
			actorName = "system"
		}
	}
	_, err := p.St.AuditAppend(store.AuditRecord{Category: cat, ActorID: actorID, ActorName: actorName, ActorRole: role,
		Action: action, Target: target, Outcome: outcome, Detail: detail, IP: ip})
	return err
}

// AuditPage returns platform audit records, newest first.
func (p *Platform) AuditPage(before int64, limit int) ([]store.AuditRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT seq,ts,category,actor_id,actor_name,actor_role,action,target,outcome,detail,ip,prev_hash,hash FROM audit_log`
	args := []any{}
	if before > 0 {
		q += ` WHERE seq<?`
		args = append(args, before)
	}
	q += ` ORDER BY seq DESC LIMIT ` + strconv.Itoa(limit)
	rows, err := p.St.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.AuditRecord{}
	for rows.Next() {
		var r store.AuditRecord
		if err := rows.Scan(&r.Seq, &r.TS, &r.Category, &r.ActorID, &r.ActorName, &r.ActorRole, &r.Action, &r.Target, &r.Outcome, &r.Detail, &r.IP, &r.PrevHash, &r.Hash); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
