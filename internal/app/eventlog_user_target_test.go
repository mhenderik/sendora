package app

import (
	"fmt"
	"strings"
	"testing"
)

func TestLogNamesTheTargetUser(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	e.want(e.do("PUT", "/api/settings", admin, map[string]any{"logging_enabled": true}), 200, "enable logging")
	e.mkUser(admin, "victim", false)
	var id int64
	if err := e.s.St.DB.QueryRow(`SELECT id FROM users WHERE username='victim'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.want(e.do("POST", fmt.Sprintf("/api/users/%d/force-reset-password", id), admin, nil), 200, "force reset")
	e.want(e.do("DELETE", fmt.Sprintf("/api/users/%d", id), admin, nil), 200, "delete user")
	body := string(e.do("GET", "/api/logs?limit=100", admin, nil).Body)
	want := fmt.Sprintf("user#%d (victim)", id)
	if strings.Count(body, want) < 3 { // created via admin API, force reset, deleted
		t.Fatalf("log entries should name the target user %q: %s", want, body)
	}
}
