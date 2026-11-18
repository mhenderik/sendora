package app

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func logsOf(e *env, tok, query string) resp {
	e.t.Helper()
	r := e.do("GET", "/api/logs"+query, tok, nil)
	e.want(r, 200, "logs")
	return r
}

func logActions(e *env, tok string) []string {
	e.t.Helper()
	var out []string
	for _, x := range logsOf(e, tok, "?limit=100").J()["logs"].([]any) {
		out = append(out, x.(map[string]any)["action"].(string))
	}
	return out
}

func hasAction(list []string, a string) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}

func enableLogs(e *env, admin string) {
	e.t.Helper()
	e.want(e.do("PUT", "/api/settings", admin, map[string]any{"logging_enabled": true}), 200, "enable logging")
}

func rowCount(e *env) (n int) {
	e.s.flushEvents()
	e.s.St.DB.QueryRow(`SELECT COUNT(*) FROM event_log`).Scan(&n)
	return
}

func TestEventLogDisabledStoresNothing(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	e.mkUser(admin, "bob", false)
	e.do("POST", "/api/auth/login", "", map[string]any{"username": "nobody", "password": "wrongwrong"})
	e.want(e.share(admin, map[string]string{"a.txt": "abc"}, nil), 201, "share")
	if n := rowCount(e); n != 0 {
		t.Fatalf("disabled logging stored %d rows", n)
	}
	r := logsOf(e, admin, "")
	if r.J()["enabled"] != false || r.J()["total"].(float64) != 0 || r.J()["retention_hours"].(float64) != 24 {
		t.Fatalf("unexpected disabled response %s", r.Body)
	}
}

func TestEventLogRecordsEventsWithoutSecrets(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	enableLogs(e, admin)
	bob := e.mkUser(admin, "bob", false)
	secretTyped := "TypedPassw0rdInUsername"
	e.want(e.do("POST", "/api/auth/login", "", map[string]any{"username": secretTyped, "password": "x1x1x1x1"}), 401, "unknown login")
	e.want(e.do("POST", "/api/auth/login", "", map[string]any{"username": "bob", "password": "WrongPassword9"}), 401, "bad pw")
	sh := e.share(bob, map[string]string{"a.txt": "abc"}, map[string]any{"password": "SharePw12345"})
	e.want(sh, 201, "share")
	id := fmt.Sprint(int64(sh.J()["id"].(float64)))
	path := "/api/shares/" + id
	e.want(e.do("POST", path+"/extend", bob, map[string]any{"days": 2}), 200, "extend")
	e.want(e.do("POST", path+"/download-limit", bob, map[string]any{"limit": 5}), 200, "limit")
	e.want(e.do("PUT", path, bob, map[string]any{"name": "SecretNewName", "password": "AnotherPw98765"}), 200, "edit")
	e.want(e.do("POST", path+"/expire", bob, nil), 200, "expire")
	e.want(e.do("POST", path+"/extend", bob, map[string]any{"days": 1}), 200, "extend again")
	e.want(e.do("GET", "/api/shares/"+sh.S("long_id")+"/download?token=x", "", nil), 401, "locked")
	sh2 := e.share(bob, map[string]string{"b.txt": "xyz"}, nil)
	e.want(sh2, 201, "share2")
	e.want(e.do("GET", "/api/shares/"+sh2.S("long_id")+"/download", "", nil), 200, "download")
	e.want(e.do("DELETE", "/api/shares/"+fmt.Sprint(int64(sh2.J()["id"].(float64))), bob, nil), 200, "delete share")
	e.want(e.do("POST", "/api/users/2/force-reset-password", admin, nil), 200, "force reset")
	e.want(e.do("POST", "/api/users/2/unlock-login", admin, nil), 200, "unlock")
	e.want(e.do("PUT", "/api/users/2", admin, map[string]any{"name": "Bob", "email": "bob@x.io"}), 200, "update user")
	inv := e.do("POST", "/api/reverse-shares/invite", admin, map[string]any{"name": "g", "open": true, "max_uploads": 2})
	e.want(inv, 201, "invite")
	e.want(e.do("POST", "/api/reverse-shares/invites/"+fmt.Sprint(int64(inv.J()["id"].(float64)))+"/revoke", admin, nil), 200, "revoke")
	e.want(e.do("PUT", "/api/settings", admin, map[string]any{"application_name": "SecretAppName", "smtp_pass": "smtp-secret-pw"}), 200, "settings")
	b := e.do("POST", "/api/backups", admin, nil)
	e.want(b, 201, "backup")
	e.want(e.do("DELETE", "/api/backups/"+b.S("name"), admin, nil), 200, "delete backup")
	e.want(e.do("DELETE", "/api/users/2", admin, nil), 200, "delete user")

	acts := logActions(e, admin)
	for _, want := range []string{"login_ok", "login_failed", "share_created", "share_extended", "share_limit_changed", "share_edited",
		"share_expired_manual", "download_started", "share_deleted", "user_created", "user_updated", "user_deleted", "user_force_reset",
		"user_unlocked", "invite_created", "invite_revoked", "settings_changed", "backup_created", "backup_deleted"} {
		if !hasAction(acts, want) {
			t.Errorf("missing action %s in %v", want, acts)
		}
	}
	e.s.flushEvents()
	rows, err := e.s.St.DB.Query(`SELECT actor_name,action,target,detail FROM event_log`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	unknown := false
	for rows.Next() {
		var a, ac, tg, d string
		rows.Scan(&a, &ac, &tg, &d)
		line := strings.Join([]string{a, ac, tg, d}, "|")
		for _, bad := range []string{secretTyped, "WrongPassword9", "SharePw12345", "AnotherPw98765", "SecretNewName", "SecretAppName", "smtp-secret-pw", "ChangedPass123", "127.0.0.1", "::1", sh.S("long_id")} {
			if strings.Contains(line, bad) {
				t.Errorf("row leaks %q: %s", bad, line)
			}
		}
		if ac == "login_failed" && a == "unknown" {
			unknown = true
		}
		if len(d) > 200 {
			t.Errorf("detail too long")
		}
	}
	if !unknown {
		t.Error("failed login for unknown account should be actor unknown")
	}
}

func TestEventLogLockoutAndOrder(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	enableLogs(e, admin)
	e.mkUser(admin, "carol", false)
	for i := 0; i < 5; i++ {
		e.want(e.do("POST", "/api/auth/login", "", map[string]any{"username": "carol", "password": "badbadbad1"}), 401, "bad")
	}
	r := logsOf(e, admin, "?q=lockout")
	if r.J()["total"].(float64) != 1 {
		t.Fatalf("want one lockout, got %s", r.Body)
	}
	all := logsOf(e, admin, "?limit=100").J()["logs"].([]any)
	if all[0].(map[string]any)["id"].(float64) < all[len(all)-1].(map[string]any)["id"].(float64) {
		t.Fatal("not newest first")
	}
}

func TestEventLogRetentionPurgeAndCap(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	enableLogs(e, admin)
	e.want(e.do("PUT", "/api/settings", admin, map[string]any{"log_retention_hours": 2}), 200, "retention")
	now := time.Now().UTC()
	db := e.s.St.DB
	db.Exec(`INSERT INTO event_log(created_at,actor_name,action) VALUES(?,?,?)`, now.Add(-3*time.Hour), "x", "old_event")
	db.Exec(`INSERT INTO event_log(created_at,actor_name,action) VALUES(?,?,?)`, now.Add(-30*time.Minute), "x", "recent_event")
	// Read side hides rows outside the window even before the purge ran.
	if r := logsOf(e, admin, "?q=old_event"); r.J()["total"].(float64) != 0 {
		t.Fatal("expired row visible")
	}
	e.s.purgeEventLog()
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE action='old_event'`).Scan(&n)
	if n != 0 {
		t.Fatal("old row not purged")
	}
	db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE action='recent_event'`).Scan(&n)
	if n != 1 {
		t.Fatal("recent row purged")
	}
	// Invalid stored retention fails closed to 1h and never exceeds 24h.
	db.Exec(`INSERT INTO event_log(created_at,actor_name,action) VALUES(?,?,?)`, now.Add(-90*time.Minute), "x", "ninety_min")
	for _, bad := range []string{"garbage", "0", "9999", "-5"} {
		e.s.St.SetSetting("log_retention_hours", bad)
		if st := e.s.refreshLogState(); st.retention != 1 {
			t.Fatalf("retention %q -> %d", bad, st.retention)
		}
	}
	e.s.purgeEventLog()
	db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE action='ninety_min'`).Scan(&n)
	if n != 0 {
		t.Fatal("fail-closed retention not applied")
	}
	// Row cap, oldest first.
	e.s.St.SetSetting("log_retention_hours", "24")
	if _, err := db.Exec(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i<?)
		INSERT INTO event_log(created_at,actor_name,action,target) SELECT ?,'x','bulk',i FROM c`, eventLogMaxRows+10, now); err != nil {
		t.Fatal(err)
	}
	e.s.purgeEventLog()
	db.QueryRow(`SELECT COUNT(*) FROM event_log`).Scan(&n)
	if n != eventLogMaxRows {
		t.Fatalf("cap: %d rows", n)
	}
	var minTarget int
	db.QueryRow(`SELECT MIN(CAST(target AS INTEGER)) FROM event_log WHERE action='bulk'`).Scan(&minTarget)
	if minTarget < 11 {
		t.Fatalf("oldest rows were not dropped first: %d", minTarget)
	}
}

func TestEventLogWipedWhenDisabled(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	enableLogs(e, admin)
	e.mkUser(admin, "dave", false)
	if rowCount(e) == 0 {
		t.Fatal("expected rows")
	}
	e.want(e.do("PUT", "/api/settings", admin, map[string]any{"logging_enabled": false}), 200, "disable")
	if n := rowCount(e); n != 0 {
		t.Fatalf("rows survive disable: %d", n)
	}
	e.mkUser(admin, "erin", false)
	if n := rowCount(e); n != 0 {
		t.Fatalf("rows written while disabled: %d", n)
	}
}

func TestEventLogEnabledViaSetSetting(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	e.s.St.SetSetting("logging_enabled", "true")
	e.mkUser(admin, "fred", false)
	if rowCount(e) == 0 {
		t.Fatal("SetSetting enable not honoured")
	}
	e.s.St.SetSetting("logging_enabled", "maybe") // invalid fails closed
	e.mkUser(admin, "gina", false)
	before := rowCount(e)
	e.mkUser(admin, "hank", false)
	if rowCount(e) != before {
		t.Fatal("invalid flag must disable logging")
	}
}

func TestEventLogAdminOnly(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	user := e.mkUser(admin, "ivy", false)
	e.want(e.do("GET", "/api/logs", "", nil), 401, "anonymous")
	e.want(e.do("GET", "/api/logs", user, nil), 403, "non-admin")
	e.want(e.do("GET", "/api/logs", admin, nil), 200, "admin")
}

func TestEventLogPaginationAndFilter(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	enableLogs(e, admin)
	for i := 0; i < 130; i++ {
		e.s.logEvent(0, "tester", "bulk_event", fmt.Sprintf("t%d", i), "")
	}
	e.s.logEvent(0, "zed", "special_100%", "x", "")
	e.s.flushEvents()
	r := logsOf(e, admin, "")
	if len(r.J()["logs"].([]any)) != 50 || r.J()["total"].(float64) < 131 {
		t.Fatalf("default page: %d total %v", len(r.J()["logs"].([]any)), r.J()["total"])
	}
	if n := len(logsOf(e, admin, "?limit=1000").J()["logs"].([]any)); n != 100 {
		t.Fatalf("limit cap: %d", n)
	}
	p2 := logsOf(e, admin, "?limit=100&offset=100").J()["logs"].([]any)
	if len(p2) < 30 {
		t.Fatalf("offset page: %d", len(p2))
	}
	if r := logsOf(e, admin, "?q=ZED"); r.J()["total"].(float64) != 1 {
		t.Fatalf("actor filter: %s", r.Body)
	}
	if r := logsOf(e, admin, "?q=100%25"); r.J()["total"].(float64) != 1 {
		t.Fatalf("wildcard must be literal: %s", r.Body)
	}
	if r := logsOf(e, admin, "?q=t129"); r.J()["total"].(float64) != 1 {
		t.Fatalf("target filter: %s", r.Body)
	}
	e.want(e.do("GET", "/api/logs?limit=0", admin, nil), 400, "limit 0")
	e.want(e.do("GET", "/api/logs?offset=-1", admin, nil), 400, "negative offset")
	// field length bounds
	e.s.logEvent(5, "n", "a", "t", strings.Repeat("é", 500))
	e.s.flushEvents()
	var d string
	e.s.St.DB.QueryRow(`SELECT detail FROM event_log WHERE action='a'`).Scan(&d)
	if len([]rune(d)) != 200 {
		t.Fatalf("detail not clipped: %d", len([]rune(d)))
	}
}

func TestEventLogSettingsValidation(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	for _, bad := range []any{0, 25, 1.5, "x", "", -1, nil, []int{1}, true} {
		r := e.do("PUT", "/api/settings", admin, map[string]any{"application_name": "ShouldRollBack", "log_retention_hours": bad})
		e.want(r, 400, fmt.Sprintf("retention %v", bad))
		if e.s.St.Setting("application_name") == "ShouldRollBack" {
			t.Fatalf("batch not atomic for %v", bad)
		}
	}
	for _, bad := range []any{"yes", 1, "", nil} {
		e.want(e.do("PUT", "/api/settings", admin, map[string]any{"application_name": "ShouldRollBack", "logging_enabled": bad}), 400, fmt.Sprintf("flag %v", bad))
	}
	if e.s.St.Setting("application_name") == "ShouldRollBack" || e.s.St.Setting("log_retention_hours") != "24" || e.s.St.Setting("logging_enabled") != "false" {
		t.Fatal("rejected batch changed state")
	}
	e.want(e.do("PUT", "/api/settings", admin, map[string]any{"log_retention_hours": 24, "logging_enabled": "true"}), 200, "valid")
	e.want(e.do("PUT", "/api/settings", admin, map[string]any{"log_retention_hours": "1"}), 200, "valid string")
	if e.s.St.Setting("log_retention_hours") != "1" || logsOf(e, admin, "").J()["retention_hours"].(float64) != 1 {
		t.Fatal("retention not applied")
	}
}

func TestEventLogConcurrentNoDeadlock(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	enableLogs(e, admin)
	bob := e.mkUser(admin, "bob", false)
	sh := e.share(bob, map[string]string{"a.txt": "abc"}, nil)
	e.want(sh, 201, "share")
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := 0; i < 24; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				switch i % 4 {
				case 0:
					e.do("GET", "/api/shares/"+sh.S("long_id")+"/download", "", nil)
				case 1:
					e.do("GET", "/api/logs?q=download", admin, nil)
				case 2:
					e.do("POST", "/api/shares/"+fmt.Sprint(int64(sh.J()["id"].(float64)))+"/extend", bob, map[string]any{"days": 1})
				case 3:
					e.do("PUT", "/api/settings", admin, map[string]any{"application_name": fmt.Sprintf("n%d", i)})
				}
			}(i)
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("deadlock with logging enabled")
	}
	if rowCount(e) == 0 {
		t.Fatal("no events recorded")
	}
}
