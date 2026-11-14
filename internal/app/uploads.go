package app

import (
	"database/sql"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

type uploadFileReq struct {
	Name     string `json:"name"`
	FullPath string `json:"full_path"`
	Size     int64  `json:"size"`
	Type     string `json:"type"`
	SHA256   string `json:"sha256"`
}

func cleanRel(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	return p
}

// validateUploadPath checks portable filename hazards before cleanRel can hide
// them. Historical POSIX absolute/dot-path normalization remains supported;
// backslash-relative folder paths are also accepted and normalized by cleanRel.
func validateUploadPath(p string) error {
	if strings.HasPrefix(p, "\\") {
		return fmt.Errorf("unsafe file path")
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "//") || strings.ContainsAny(p, ":\"*?<>|") {
		return fmt.Errorf("unsafe file path")
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return fmt.Errorf("unsafe file path")
		}
	}
	for _, component := range strings.Split(p, "/") {
		if component == "" || component == "." || component == ".." {
			continue
		}
		if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return fmt.Errorf("unsafe file path")
		}
		// Windows reserves device names even with an extension, in any folder.
		base, _, _ := strings.Cut(component, ".")
		base = strings.ToUpper(strings.TrimRight(base, " "))
		switch base {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
			return fmt.Errorf("unsafe file path")
		}
		if strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT") {
			suffix := base[3:]
			if len(suffix) == 1 && suffix[0] >= '1' && suffix[0] <= '9' || suffix == "¹" || suffix == "²" || suffix == "³" {
				return fmt.Errorf("unsafe file path")
			}
		}
	}
	return nil
}

// Persisted archive names must already be safe, canonical relative paths. Never
// silently rewrite legacy metadata: a ZIP must preserve exactly the safe name.
func validateArchivePath(p string) error {
	if p == "" || p != cleanRel(p) || validateUploadPath(p) != nil {
		return fmt.Errorf("%w: unsafe archive path", errIntegrity)
	}
	return nil
}

var uploadIDRe = regexp.MustCompile(`^[a-f0-9]{16,64}$`)

// uploadDir maps a session id to its staging directory. Ids are always generated
// hex strings; anything else (e.g. from a tampered database) maps to a fixed
// directory that is never created, so path tricks cannot reach other places.
func (s *Server) uploadDir(id string) string {
	if !uploadIDRe.MatchString(id) {
		id = ".invalid-session"
	}
	return filepath.Join(s.St.DataDir, "uploads", id)
}

func (s *Server) ownUpload(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	var uid int64
	if s.St.DB.QueryRow(`SELECT user_id FROM upload_sessions WHERE id=?`, id).Scan(&uid) != nil || uid != s.ctxUser(r).ID {
		jerr(w, 404, "upload not found")
		return "", false
	}
	return id, true
}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	u := s.ctxUser(r)
	var b struct {
		Files   []uploadFileReq
		ShareID *int64 `json:"share_id"`
	}
	if decode(r, &b) != nil || len(b.Files) == 0 || len(b.Files) > 5000 {
		jerr(w, 400, "files required (max 5000)")
		return
	}
	if b.ShareID != nil && u.IsGuest {
		jerr(w, 404, "share not found")
		return
	}
	var total int64
	for _, f := range b.Files {
		if f.SHA256 != "" && !validSHA256(f.SHA256) {
			jerr(w, 400, "invalid SHA-256 checksum")
			return
		}
		if f.Size < 0 || f.Size > math.MaxInt64-total {
			jerr(w, 400, "bad size")
			return
		}
		total += f.Size
	}
	if max := int64(s.St.SettingInt("max_share_size", 2048)) * 1024 * 1024; max > 0 && total > max {
		jerr(w, 413, fmt.Sprintf("total size exceeds limit of %d MB", max/1024/1024))
		return
	}
	maxShare := int64(s.St.SettingInt("max_share_size", 2048)) * 1024 * 1024
	id := randHex(16)
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	// The first statement obtains SQLite's write lock before the snapshot. Both
	// attribution and capacity are revalidated here, including different guests
	// spending the same inviter's quota. Never call Store.DB while tx is open.
	res, err := tx.Exec(`UPDATE users SET quota_bytes=quota_bytes WHERE id=? AND is_guest=?`, u.ID, u.IsGuest)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		jerr(w, 403, "user no longer valid")
		return
	}
	var pending int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM upload_sessions WHERE user_id=?`, u.ID).Scan(&pending); err != nil {
		jerr(w, 500, "database error")
		return
	}
	if pending >= 16 || (u.IsGuest && pending > 0) {
		jerr(w, 429, "too many active uploads")
		return
	}
	owner := u.ID
	var inviteID any
	if u.IsGuest {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM reverse_invites WHERE guest_user_id=?`, u.ID).Scan(&count); err != nil || count != 1 {
			jerr(w, 403, "invite no longer valid")
			return
		}
		var iid int64
		if err := tx.QueryRow(`SELECT r.id,r.user_id FROM reverse_invites r JOIN users o ON o.id=r.user_id AND o.is_guest=0
			WHERE r.guest_user_id=? AND r.completed_at IS NULL AND r.expires_at>?`, u.ID, time.Now()).Scan(&iid, &owner); err != nil {
			jerr(w, 403, "invite no longer valid")
			return
		}
		inviteID = iid
	}
	var shareID any
	if b.ShareID != nil {
		// Appending: the write lock is already held, so the share cannot be
		// removed or resized between this check and the session insert.
		var lid string
		var status string
		if err := tx.QueryRow(`SELECT long_id,status FROM shares WHERE id=? AND user_id=?`, *b.ShareID, u.ID).Scan(&lid, &status); err != nil {
			jerr(w, 404, "share not found")
			return
		}
		if status == "deleted" || !shareDirExists(s.St.DataDir, lid) {
			jerr(w, 409, "share is no longer available")
			return
		}
		var existing int64
		if err := tx.QueryRow(`SELECT COALESCE(SUM(size),0) FROM files WHERE share_id=?`, *b.ShareID).Scan(&existing); err != nil {
			jerr(w, 500, "database error")
			return
		}
		if msg := shareTotalProblem(existing, total, maxShare); msg != "" {
			jerr(w, 413, msg)
			return
		}
		shareID = *b.ShareID
	}
	usage, err := s.St.StorageUsageTx(tx, owner)
	if err != nil {
		jerr(w, 503, "storage accounting unavailable")
		return
	}
	if usage.StoredBytes+usage.ReservedBytes > usage.QuotaBytes || total > usage.AvailableBytes {
		jerr(w, 413, "storage quota exceeded")
		return
	}
	if _, err = tx.Exec(`INSERT INTO upload_sessions(id,user_id,invite_id,quota_owner_id,share_id) VALUES(?,?,?,?,?)`, id, u.ID, inviteID, owner, shareID); err != nil {
		jerr(w, 500, "upload metadata write failed")
		return
	}
	for i, f := range b.Files {
		if validateUploadPath(f.FullPath) != nil {
			jerr(w, 400, "unsafe file path")
			return
		}
		fp := cleanRel(f.FullPath)
		if fp == "" {
			if validateUploadPath(f.Name) != nil {
				jerr(w, 400, "unsafe file path")
				return
			}
			fp = cleanRel(f.Name)
		}
		if validateArchivePath(fp) != nil {
			jerr(w, 400, "unsafe file path")
			return
		}
		if err := validateNewPath(f.FullPath); err != nil {
			jerr(w, 400, err.Error())
			return
		}
		if err := validateNewPath(fp); err != nil {
			jerr(w, 400, err.Error())
			return
		}
		name := safeName(path.Base(fp))
		if name == "" {
			tx.Rollback()
			jerr(w, 400, "invalid file name")
			return
		}
		typ := f.Type
		if typ == "" {
			typ = mime.TypeByExtension(filepath.Ext(name))
		}
		if _, err := tx.Exec(`INSERT INTO upload_files(session_id,idx,name,full_path,size,type,sha256) VALUES(?,?,?,?,?,?,?)`, id, i, name, fp, f.Size, typ, f.SHA256); err != nil {
			tx.Rollback()
			jerr(w, 500, "upload metadata write failed")
			return
		}
	}
	if err := os.MkdirAll(s.uploadDir(id), 0o700); err != nil {
		jerr(w, 503, "upload storage unavailable")
		return
	}
	if err := tx.Commit(); err != nil {
		os.Remove(s.uploadDir(id))
		jerr(w, 500, "upload metadata write failed")
		return
	}
	jout(w, 201, map[string]any{"id": id})
}

func (s *Server) uploadStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownUpload(w, r)
	if !ok {
		return
	}
	rows, _ := s.St.DB.Query(`SELECT idx,size,received FROM upload_files WHERE session_id=? ORDER BY idx`, id)
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var idx int
		var size, rec int64
		rows.Scan(&idx, &size, &rec)
		out = append(out, map[string]any{"idx": idx, "size": size, "received": rec})
	}
	jout(w, 200, map[string]any{"id": id, "files": out})
}

// uploadChunk appends request body at Upload-Offset. Resumable: client asks status then continues from received.
func (s *Server) uploadChunk(w http.ResponseWriter, r *http.Request) {
	unlock := lockStripe(&s.uploadLocks, chi.URLParam(r, "id"))
	defer unlock()
	id, ok := s.ownUpload(w, r)
	if !ok {
		return
	}
	idx, _ := strconv.Atoi(chi.URLParam(r, "idx"))
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		jerr(w, 400, "Upload-Offset required")
		return
	}
	var size, rec int64
	if s.St.DB.QueryRow(`SELECT size,received FROM upload_files WHERE session_id=? AND idx=?`, id, idx).Scan(&size, &rec) != nil {
		jerr(w, 404, "file not found")
		return
	}
	if offset != rec {
		jout(w, 409, map[string]any{"error": "offset mismatch", "received": rec})
		return
	}
	if rec >= size && size > 0 {
		jout(w, 200, map[string]any{"received": rec})
		return
	}
	part := filepath.Join(s.uploadDir(id), strconv.Itoa(idx)+".part")
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		jerr(w, 500, "storage error")
		return
	}
	defer f.Close()
	f.Seek(offset, io.SeekStart)
	n, err := io.Copy(f, io.LimitReader(r.Body, min(size-offset, int64(64<<20))+1))
	if offset+n > size || n > 64<<20 {
		f.Truncate(offset)
		jerr(w, 413, "chunk exceeds declared size")
		return
	}
	if n > 0 {
		s.St.DB.Exec(`UPDATE upload_files SET received=? WHERE session_id=? AND idx=?`, offset+n, id, idx)
	}
	if err != nil {
		jerr(w, 500, "write failed")
		return
	}
	jout(w, 200, map[string]any{"received": offset + n})
}

func (s *Server) abortUpload(w http.ResponseWriter, r *http.Request) {
	unlock := lockStripe(&s.uploadLocks, chi.URLParam(r, "id"))
	defer unlock()
	id, ok := s.ownUpload(w, r)
	if !ok {
		return
	}
	if err := s.removeUploadSession(id); err != nil {
		jerr(w, 503, "upload cleanup failed")
		return
	}
	jout(w, 200, map[string]any{"ok": true})
}

type completeReq struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Password      string   `json:"password"`
	ExpiresInDays int      `json:"expires_in_days"`
	DownloadLimit int64    `json:"download_limit"`
	Recipients    []string `json:"recipients"`
	Message       string   `json:"message"`
	EmailLanguage string   `json:"email_language"`
}

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) {
	s.completeUploadWithShareID(w, r, newShareID)
}

func (s *Server) completeUploadWithShareID(w http.ResponseWriter, r *http.Request, generate func() (string, error)) {
	unlock := lockStripe(&s.uploadLocks, chi.URLParam(r, "id"))
	defer unlock()
	id, ok := s.ownUpload(w, r)
	if !ok {
		return
	}
	u := s.ctxUser(r)
	var appendTo sql.NullInt64
	if err := s.St.DB.QueryRow(`SELECT share_id FROM upload_sessions WHERE id=?`, id).Scan(&appendTo); err != nil {
		jerr(w, 409, "upload no longer exists")
		return
	}
	if appendTo.Valid {
		s.completeAppend(w, r, id, appendTo.Int64)
		return
	}
	var b completeReq
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
	if len(b.Recipients) > 20 {
		jerr(w, 400, "max 20 recipients")
		return
	}
	if validateText(b.Name, maxShareNameRunes, true) != nil || validateText(b.Description, maxDescriptionRunes, false) != nil || validateText(b.Message, maxMessageRunes, false) != nil {
		jerr(w, 400, "name, description or message too long or contains invalid characters")
		return
	}
	type uf struct {
		idx                           int
		name, fp, typ                 string
		expected, checksum, integrity string
		size, rec                     int64
	}
	rows, err := s.St.DB.Query(`SELECT idx,name,full_path,type,size,received,sha256 FROM upload_files WHERE session_id=? ORDER BY idx`, id)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	var files []uf
	for rows.Next() {
		var f uf
		if err := rows.Scan(&f.idx, &f.name, &f.fp, &f.typ, &f.size, &f.rec, &f.expected); err != nil {
			rows.Close()
			jerr(w, 500, "database error")
			return
		}
		files = append(files, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(files) == 0 {
		jerr(w, 500, "upload metadata unavailable")
		return
	}
	for _, f := range files {
		if f.rec != f.size {
			jerr(w, 400, fmt.Sprintf("file %q incomplete", f.name))
			return
		}
	}
	max := s.St.SettingInt("max_expiry_time", 30)
	days := b.ExpiresInDays
	if days <= 0 {
		days = s.St.SettingInt("default_expiry_time", 7)
	}
	if max > 0 && days > max {
		days = max
	}
	var owner int64
	var reservationInvite sql.NullInt64
	if err := s.St.DB.QueryRow(`SELECT quota_owner_id,invite_id FROM upload_sessions WHERE id=? AND user_id=?`, id, u.ID).Scan(&owner, &reservationInvite); err != nil {
		jerr(w, 409, "upload reservation unavailable")
		return
	}
	var inviteID any
	var inviteOwner int64
	if u.IsGuest {
		if !reservationInvite.Valid {
			jerr(w, 403, "invite no longer valid")
			return
		}
		inviteID, inviteOwner = reservationInvite.Int64, owner
		b.Password, b.Recipients = "", nil
	} else if reservationInvite.Valid || owner != u.ID {
		jerr(w, 409, "upload reservation invalid")
		return
	}
	if !u.IsGuest {
		if n := countRecipients(b.Recipients); n > 0 && !s.takeMail(u.ID, n) {
			w.Header().Set("Retry-After", "3600")
			jerr(w, 429, "too many emails sent recently; try again later")
			return
		}
		policy, err := s.loadSharePolicy()
		if err != nil {
			jerr(w, 500, "share password policy unavailable")
			return
		}
		if err := validateSharePassword(policy, b.Password); err != nil {
			jerr(w, 400, err.Error())
			return
		}
	}
	var hash string
	if b.Password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(b.Password), bcrypt.DefaultCost)
		if err != nil {
			jerr(w, 400, "share password must not exceed 72 bytes")
			return
		}
		hash = string(h)
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		name = "Shared files"
	}
	var lim any
	if b.DownloadLimit > 0 {
		lim = b.DownloadLimit
	}
	longID, dest, err := claimShareDir(s.St.DB, filepath.Join(s.St.DataDir, "shares"), generate)
	if err != nil {
		jerr(w, 500, "share storage unavailable")
		return
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(dest)
		}
	}()
	// Keep the original parts until the database commit succeeds, so every failure is retryable.
	for i := range files {
		f := &files[i]
		disk := filepath.Join(dest, fmt.Sprintf("%d_%s", f.idx, f.name))
		if err := stageUploadFile(filepath.Join(s.uploadDir(id), strconv.Itoa(f.idx)+".part"), disk, f.size); err != nil {
			jerr(w, 500, "storage error")
			return
		}
		checksum, err := hashDisk(r.Context(), disk, f.size)
		if err != nil {
			jerr(w, 409, "uploaded file integrity check failed")
			return
		}
		if f.expected != "" && checksum != f.expected {
			jerr(w, 409, "upload checksum mismatch")
			return
		}
		f.checksum, f.integrity = checksum, "server_baseline"
		if f.expected != "" {
			f.integrity = "expected_checksum_verified"
		}
	}
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	// An admitted reservation may finish after the quota is lowered. Only its
	// original owner may receive the share; never reattribute it at completion.
	res, err := tx.Exec(`UPDATE upload_sessions SET id=id WHERE id=? AND user_id=? AND quota_owner_id=?
		AND EXISTS(SELECT 1 FROM users WHERE id=? AND is_guest=?)
		AND EXISTS(SELECT 1 FROM users WHERE id=? AND is_guest=0)`, id, u.ID, owner, u.ID, u.IsGuest, owner)
	if err != nil {
		jerr(w, 500, "upload finalization failed")
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		jerr(w, 409, "upload no longer exists")
		return
	}
	if inviteID != nil {
		var valid int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM reverse_invites WHERE id=? AND guest_user_id=? AND user_id=? AND completed_at IS NULL AND expires_at>?`, inviteID, u.ID, owner, time.Now()).Scan(&valid); err != nil || valid != 1 {
			jerr(w, 403, "invite no longer valid")
			return
		}
	}
	res, err = tx.Exec(`INSERT INTO shares(user_id,invite_id,long_id,name,description,password_hash,download_limit,expires_at) VALUES(?,?,?,?,?,?,?,?)`,
		owner, inviteID, longID, name, b.Description, hash, lim, time.Now().AddDate(0, 0, days))
	if err != nil {
		tx.Rollback()
		jerr(w, 500, "share metadata write failed")
		return
	}
	sid, _ := res.LastInsertId()
	for _, f := range files {
		disk := filepath.Join(dest, fmt.Sprintf("%d_%s", f.idx, f.name))
		if _, err := tx.Exec(`INSERT INTO files(share_id,name,full_path,size,type,disk_path,sha256,integrity) VALUES(?,?,?,?,?,?,?,?)`, sid, f.name, f.fp, f.size, f.typ, disk, f.checksum, f.integrity); err != nil {
			jerr(w, 500, "file metadata write failed")
			return
		}
	}
	if inviteID != nil {
		res, err := tx.Exec(`UPDATE reverse_invites SET completed_at=?, used=1 WHERE id=? AND completed_at IS NULL AND expires_at>?`, time.Now(), inviteID, time.Now())
		if err != nil {
			jerr(w, 500, "invite update failed")
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			jerr(w, 403, "invite no longer valid")
			return
		}
	}
	res, err = tx.Exec(`DELETE FROM upload_sessions WHERE id=?`, id)
	if err != nil {
		jerr(w, 500, "upload finalization failed")
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		jerr(w, 409, "upload no longer exists")
		return
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "share metadata write failed")
		return
	}
	committed = true
	os.RemoveAll(s.uploadDir(id))
	s.logEvent(u.ID, u.Username, "share_created", "share#"+strconv.FormatInt(sid, 10), fmt.Sprintf("%d files", len(files)))
	url := s.baseURL(r) + "/share/" + longID
	emailBase, emailOK := s.emailBaseURL(r)
	emailURL := emailBase + "/share/" + longID
	sh, _ := s.shareByID(sid)
	for _, rcpt := range b.Recipients {
		rcpt = strings.TrimSpace(rcpt)
		if rcpt != "" && emailOK {
			s.queueMail(rcpt, u.Name+" shared files with you", "tpl_share_created", map[string]any{
				"EmailLanguage": b.EmailLanguage, "RecipientName": rcpt, "SenderName": nameOf(u), "URL": emailURL, "Message": b.Message,
				"Expires": sh.ExpiresAt.Format("2006-01-02"), "ShareName": name,
			})
		}
	}
	if inviteID != nil && emailOK {
		go s.notifyInviteCompleted(inviteOwner, u, emailURL, name)
	}
	jout(w, 201, map[string]any{"id": sid, "long_id": longID, "url": url})
}

// LockUserUploads blocks chunks, completion, abort and stale cleanup while a
// user (including their invited guests) is deleted. Lock all stripes: discovering
// a subset first would miss sessions admitted immediately before deletion.
func (s *Server) LockUserUploads(_ int64) func() {
	for i := range s.uploadLocks {
		s.uploadLocks[i].Lock()
	}
	return func() {
		for i := len(s.uploadLocks) - 1; i >= 0; i-- {
			s.uploadLocks[i].Unlock()
		}
	}
}

// UserUploadSessions is a convenience snapshot. Deletion must collect the same
// predicate inside its transaction before deleting the user, then clean disk.
func (s *Server) UserUploadSessions(userID int64) ([]string, error) {
	rows, err := s.St.DB.Query(`SELECT id FROM upload_sessions WHERE user_id=? OR quota_owner_id=?`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Caller holds the upload stripe. Keep the reservation until disk cleanup
// succeeds; failed cleanup must not grant storage for another upload.
func (s *Server) removeUploadSession(id string) error {
	if err := os.RemoveAll(s.uploadDir(id)); err != nil {
		return err
	}
	_, err := s.St.DB.Exec(`DELETE FROM upload_sessions WHERE id=?`, id)
	return err
}

func stageUploadFile(src, dest string, size int64) error {
	in, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) && size == 0 {
			return os.WriteFile(dest, nil, 0o600)
		}
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() != size {
		return errIntegrity
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, io.LimitReader(in, size+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if n != size {
		return errIntegrity
	}
	return closeErr
}

func nameOf(u *User) string {
	if u.Name != "" {
		return u.Name
	}
	return u.Username
}
