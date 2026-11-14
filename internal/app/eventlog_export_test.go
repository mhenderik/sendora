package app

import (
	"encoding/csv"
	"strings"
	"testing"
)

func TestLogsExportCSV(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	user := e.mkUser(admin, "plain", false)
	e.want(e.do("GET", "/api/logs/export", admin, nil), 409, "export refused while logging is off")
	e.want(e.do("PUT", "/api/settings", admin, map[string]any{"logging_enabled": true}), 200, "enable logging")
	e.share(admin, map[string]string{"a.txt": "hello"}, nil)
	e.s.logEvent(1, "=cmd|' /C calc'!A0", "=evil", "+target", "@detail")
	e.want(e.do("GET", "/api/logs/export", "", nil), 401, "anonymous export")
	e.want(e.do("GET", "/api/logs/export", user, nil), 403, "non-admin export")
	r := e.do("GET", "/api/logs/export", admin, nil)
	e.want(r, 200, "admin export")
	if !strings.HasPrefix(r.Hdr.Get("Content-Type"), "text/csv") || !strings.Contains(r.Hdr.Get("Content-Disposition"), "attachment") {
		t.Fatal(r.Hdr)
	}
	records, err := csv.NewReader(strings.NewReader(string(r.Body))).ReadAll()
	if err != nil || len(records) < 3 || strings.Join(records[0], ",") != "id,time_utc,user,action,target,details" {
		t.Fatal(records, err)
	}
	seenShare, seenEvil := false, false
	for _, rec := range records[1:] {
		if rec[3] == "share_created" {
			seenShare = true
		}
		if rec[2] == "'=cmd|' /C calc'!A0" && rec[3] == "'=evil" && rec[4] == "'+target" && rec[5] == "'@detail" {
			seenEvil = true
		}
		for _, cell := range rec[2:] {
			if cell != "" && strings.ContainsRune("=+-@", rune(cell[0])) {
				t.Fatal("formula cell not neutralised", cell)
			}
		}
	}
	if !seenShare || !seenEvil {
		t.Fatal(seenShare, seenEvil, records)
	}
	filtered := e.do("GET", "/api/logs/export?q=share_created", admin, nil)
	e.want(filtered, 200, "filtered export")
	rows, _ := csv.NewReader(strings.NewReader(string(filtered.Body))).ReadAll()
	for _, rec := range rows[1:] {
		if rec[3] != "share_created" {
			t.Fatal("filter ignored", rec)
		}
	}
	e.want(e.do("PUT", "/api/settings", admin, map[string]any{"logging_enabled": false}), 200, "disable logging")
	e.want(e.do("GET", "/api/logs/export", admin, nil), 409, "export after disabling")
}
