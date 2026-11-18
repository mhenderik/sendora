package app

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// RunJobs starts periodic maintenance until ctx is cancelled.
func (s *Server) RunJobs(ctx context.Context) {
	s.RunDownloadMail(ctx)
	s.auditStartup()
	s.maintain()
	t := time.NewTicker(10 * time.Minute)
	lastBackup := time.Time{}
	go func() {
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				s.auditShutdown()
				return
			case now := <-t.C:
				s.maintain()
				if s.St.SettingBool("backups_enabled") && now.Sub(lastBackup) > 24*time.Hour {
					if _, err := s.makeBackup(); err != nil {
						log.Println("backup failed:", err)
					} else {
						lastBackup = now
						s.pruneBackups()
					}
				}
			}
		}
	}()
}

func (s *Server) maintain() {
	now := time.Now()
	s.cleanImportFiles()
	// mark expired
	if res, err := s.St.DB.Exec(`UPDATE shares SET status='expired' WHERE status='active' AND ((expires_at IS NOT NULL AND expires_at<?) OR (download_limit IS NOT NULL AND download_count>=download_limit))`, now); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			s.logEvent(0, "system", "shares_expired", "", strconv.FormatInt(n, 10)+" shares")
		}
	}

	// expiry warnings
	if days := s.St.SettingInt("expiry_warning_days", 2); days > 0 {
		rows, _ := s.St.DB.Query(`SELECT s.id,s.name,s.long_id,s.expires_at,u.email FROM shares s JOIN users u ON u.id=s.user_id
			WHERE s.status='active' AND s.expiry_warned=0 AND u.email<>'' AND s.expires_at IS NOT NULL AND s.expires_at<?`, now.AddDate(0, 0, days))
		type w struct {
			id               int64
			name, lid, email string
			exp              time.Time
		}
		var ws []w
		for rows != nil && rows.Next() {
			var x w
			rows.Scan(&x.id, &x.name, &x.lid, &x.exp, &x.email)
			ws = append(ws, x)
		}
		if rows != nil {
			rows.Close()
		}
		// Same rules as every other email link: a configured public https URL
		// (or a loopback address); otherwise no expiry mail with a broken link.
		base, baseOK := s.emailBaseURL(&http.Request{Host: "invalid.invalid"})
		if !baseOK {
			ws = nil
		}
		for _, x := range ws {
			s.St.DB.Exec(`UPDATE shares SET expiry_warned=1 WHERE id=?`, x.id)
			s.queueMail(x.email, "Share expiring soon", "tpl_share_expiring", map[string]any{
				"ShareName": x.name, "URL": base + "/share/" + x.lid, "Expires": x.exp.Format("2006-01-02")})
		}
	}

	// delete files of long-expired shares
	if s.St.SettingBool("auto_clean_files") {
		keep := s.St.SettingInt("clean_files_days", 7)
		rows, _ := s.St.DB.Query(`SELECT id FROM shares WHERE status<>'active' AND COALESCE(expires_at,created_at)<?`, now.AddDate(0, 0, -keep))
		var ids []int64
		for rows != nil && rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		if rows != nil {
			rows.Close()
		}
		for _, id := range ids {
			if _, err := s.removeShareIf(id, `status<>'active' AND COALESCE(expires_at,created_at)<?`, now.AddDate(0, 0, -keep)); err != nil {
				log.Printf("expired share cleanup: %v", err)
			}
		}
	}

	// stale upload sessions (> 24h)
	// created_at defaults to SQLite's CURRENT_TIMESTAMP (UTC text) but may also be
	// written by Go (local time with an offset); datetime() normalises both to UTC.
	staleBefore := now.UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	rows, _ := s.St.DB.Query(`SELECT id FROM upload_sessions WHERE datetime(created_at)<?`, staleBefore)
	var stale []string
	for rows != nil && rows.Next() {
		var id string
		rows.Scan(&id)
		stale = append(stale, id)
	}
	if rows != nil {
		rows.Close()
	}
	for _, id := range stale {
		unlock := lockStripe(&s.uploadLocks, id)
		// The discovery query is only a snapshot. Completion/abort may already
		// have removed this session while cleanup waited for its stripe.
		var exists int
		if err := s.St.DB.QueryRow(`SELECT COUNT(*) FROM upload_sessions WHERE id=? AND datetime(created_at)<?`, id, staleBefore).Scan(&exists); err == nil && exists == 1 {
			if err := s.removeUploadSession(id); err != nil {
				log.Printf("stale upload cleanup: %v", err)
			}
		}
		unlock()
	}
	s.St.DB.Exec(`DELETE FROM revoked_access_tokens WHERE expires_at<?`, now)
	s.St.DB.Exec(`DELETE FROM verification_attempts WHERE expires_at<?`, now)
	s.St.DB.Exec(`DELETE FROM refresh_tokens WHERE expires_at<?`, now)
	s.St.DB.Exec(`DELETE FROM refresh_spent WHERE expires_at<?`, now)
	s.St.DB.Exec(`DELETE FROM password_resets WHERE expires_at<?`, now)
	s.St.DB.Exec(`DELETE FROM email_verifications WHERE expires_at<?`, now)
	s.St.DB.Exec(`DELETE FROM downloads WHERE datetime(created_at)<?`, now.UTC().AddDate(0, 0, -90).Format("2006-01-02 15:04:05"))
	// Self-registrations that never proved their email (they cannot sign in) are
	// squatting on usernames and addresses: remove them after 48 hours. Only
	// accounts still marked as a pending registration qualify, never an
	// established account whose changed address is not verified yet, nor one
	// linked to a sign-in provider (whose email may be unverified on purpose).
	if res, err := s.St.DB.Exec(`DELETE FROM users WHERE pending_registration=1 AND email_verified=0 AND is_guest=0 AND created_at<?
		AND password_hash<>''
		AND NOT EXISTS(SELECT 1 FROM user_auth_providers WHERE user_auth_providers.user_id=users.id)
		AND NOT EXISTS(SELECT 1 FROM shares WHERE shares.user_id=users.id)
		AND NOT EXISTS(SELECT 1 FROM upload_sessions WHERE upload_sessions.user_id=users.id OR upload_sessions.quota_owner_id=users.id)`,
		now.UTC().Add(-48*time.Hour).Format("2006-01-02 15:04:05")); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("removed %d unverified account(s) older than 48 hours", n)
		}
	}
	// Guests from open upload links whose single-use invitation expired unused:
	// remove the invitation and the guest account (their uploads, if any, are
	// shares owned by the inviter and stay).
	s.St.DB.Exec(`DELETE FROM users WHERE is_guest=1
		AND id IN (SELECT guest_user_id FROM reverse_invites WHERE parent_id IS NOT NULL AND completed_at IS NULL AND expires_at<?)
		AND NOT EXISTS(SELECT 1 FROM upload_sessions WHERE upload_sessions.user_id=users.id)`, now)
	s.St.DB.Exec(`DELETE FROM reverse_invites WHERE parent_id IS NOT NULL AND completed_at IS NULL AND expires_at<?
		AND guest_user_id NOT IN (SELECT id FROM users)`, now)
	s.pruneIPBans()
	s.purgeAudit()
}

// Automatic retention never removes pre-migration plaintext backups or
// pre-import backups (sendora-preimport-*): only this pattern is pruned.
var backupFile = regexp.MustCompile(`^sendora-encrypted-\d{8}-\d{6}-[a-f0-9]{12}\.db$`)

func (s *Server) pruneBackups() {
	keep := s.St.SettingInt("backup_keep", 7)
	if keep <= 0 {
		return
	}
	ents, _ := os.ReadDir(filepath.Join(s.St.DataDir, "backups"))
	var names []string
	for _, e := range ents {
		if backupFile.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		os.Remove(filepath.Join(s.St.DataDir, "backups", names[0]))
		names = names[1:]
	}
}
