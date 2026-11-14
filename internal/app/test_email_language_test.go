package app

import (
	"strings"
	"testing"

	"github.com/mhenderik/sendora/internal/store"
)

// The administrator's test mail follows the account's email language.
func TestTestEmailUsesAccountLanguage(t *testing.T) {
	for _, language := range []string{"nl", "de", "fr", "es"} {
		t.Run(language, func(t *testing.T) {
			e := newEnv(t)
			sink := newSMTP(t)
			e.useSMTP(sink)
			tok := e.setup()
			if _, err := e.s.St.DB.Exec(`UPDATE users SET language=? WHERE username='admin'`, language); err != nil {
				t.Fatal(err)
			}
			subject, body := store.TestMailText(language)
			e.want(e.do("POST", "/api/email-templates/test", tok, nil), 200, "test mail")
			raw := sink.wait(t, strings.TrimSuffix(body, "."))
			if strings.Contains(raw, "SMTP works") {
				t.Fatalf("%s: message still English:\n%s", language, raw)
			}
			if !strings.Contains(raw, "Subject:") || subject == "" {
				t.Fatalf("%s: subject missing:\n%s", language, raw)
			}
		})
	}
}
