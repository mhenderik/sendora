package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/mhenderik/sendora/internal/store"
	"io"
	"log"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
	"unicode/utf8"
)

func cleanHeader(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func (s *Server) sendMail(to, subject, body string) error {
	if err := s.smtpConfigured(); err != nil {
		return err
	}
	host := s.St.Setting("smtp_host")
	if host == "" {
		return fmt.Errorf("smtp not configured")
	}
	addr, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("bad recipient")
	}
	port := s.St.Setting("smtp_port")
	user, pass := s.St.Setting("smtp_user"), s.St.Setting("smtp_pass")
	from := s.St.Setting("smtp_from")
	if from == "" {
		from = user
	}
	fa, err := mail.ParseAddress(from)
	if err != nil {
		return fmt.Errorf("bad from address")
	}
	msg := mailMessageReplyTo(fa, addr, s.replyToAddress(), subject, body)
	hp := net.JoinHostPort(host, port)
	mode := s.St.Setting("smtp_tls")

	var c *smtp.Client
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if mode == "tls" {
		conn, err := tls.DialWithDialer(dialer, "tcp", hp, cfg)
		if err != nil {
			return err
		}
		conn.SetDeadline(time.Now().Add(30 * time.Second))
		c, err = smtp.NewClient(conn, host)
		if err != nil {
			conn.Close()
			return err
		}
	} else {
		conn, err := dialer.Dial("tcp", hp)
		if err != nil {
			return err
		}
		conn.SetDeadline(time.Now().Add(30 * time.Second))
		c, err = smtp.NewClient(conn, host)
		if err != nil {
			conn.Close()
			return err
		}
		if mode == "starttls" {
			if err := c.StartTLS(cfg); err != nil {
				c.Close()
				return err
			}
		}
	}
	defer c.Close()
	if user != "" {
		if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(fa.Address); err != nil {
		return err
	}
	if err := c.Rcpt(addr.Address); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write([]byte(msg)); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func mailMessage(from, to *mail.Address, subject, body string) string {
	return mailMessageReplyTo(from, to, nil, subject, body)
}

// replyToAddress is the optional Reply-To of all outgoing mail (nil when unset or invalid).
func (s *Server) replyToAddress() *mail.Address {
	v := strings.TrimSpace(s.St.Setting("smtp_reply_to"))
	if v == "" {
		return nil
	}
	a, err := mail.ParseAddress(v)
	if err != nil {
		return nil
	}
	return a
}

// validateReplyToSetting accepts an empty value or one address, optionally with a name.
func validateReplyToSetting(key string, val *string) (owned bool, err error) {
	if key != "smtp_reply_to" {
		return false, nil
	}
	v := strings.TrimSpace(*val)
	if v == "" {
		*val = ""
		return true, nil
	}
	if len(v) > 320 || strings.ContainsAny(v, "\r\n\x00") {
		return true, fmt.Errorf("invalid reply-to address")
	}
	a, e := mail.ParseAddress(v)
	if e != nil {
		return true, fmt.Errorf("invalid reply-to address")
	}
	// A bare address is stored without angle brackets; with a name: Name <address>.
	if a.Name == "" {
		*val = a.Address
	} else {
		*val = a.String()
	}
	return true, nil
}

func mailMessageReplyTo(from, to, replyTo *mail.Address, subject, body string) string {
	var encoded bytes.Buffer
	qp := quotedprintable.NewWriter(&encoded)
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	_, _ = qp.Write([]byte(body))
	_ = qp.Close()
	// Message-ID on the sender's own domain and Auto-Submitted mark this as a
	// well-formed automatic message, which junk filters otherwise penalise.
	domain := "localhost"
	if at := strings.LastIndexByte(from.Address, '@'); at >= 0 && at+1 < len(from.Address) {
		domain = from.Address[at+1:]
	}
	replyHeader := ""
	if replyTo != nil {
		replyHeader = "Reply-To: " + replyTo.String() + "\r\n"
	}
	return fmt.Sprintf("From: %s\r\n%sTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nAuto-Submitted: auto-generated\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s", from.String(), replyHeader, to.String(), mime.QEncoding.Encode("UTF-8", cleanHeader(subject)), time.Now().Format(time.RFC1123Z), randHex(16), domain, encoded.String())
}

func (s *Server) smtpConfigured() error {
	if strings.TrimSpace(s.St.Setting("smtp_host")) == "" {
		return fmt.Errorf("smtp not configured")
	}
	port, err := strconv.Atoi(s.St.Setting("smtp_port"))
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid SMTP port")
	}
	from := s.St.Setting("smtp_from")
	if from == "" {
		from = s.St.Setting("smtp_user")
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return fmt.Errorf("invalid SMTP sender")
	}
	mode := s.St.Setting("smtp_tls")
	if mode != "none" && mode != "tls" && mode != "starttls" {
		return fmt.Errorf("invalid SMTP TLS mode")
	}
	return nil
}

func (s *Server) recipientLanguage(to, selected string) string {
	address, err := mail.ParseAddress(to)
	if err == nil {
		var language string
		if s.St.DB.QueryRow(`SELECT language FROM users WHERE lower(trim(email))=? AND is_guest=0 AND (SELECT COUNT(*) FROM users WHERE lower(trim(email))=?)=1`, strings.ToLower(address.Address), strings.ToLower(address.Address)).Scan(&language) == nil {
			if s.St.ValidMailLanguage(language) {
				return language
			}
			return "en"
		}
	}
	if s.St.ValidMailLanguage(selected) {
		return selected
	}
	return "en"
}

var mailVars = []string{"ApplicationName", "RecipientName", "SenderName", "URL", "Message", "Expires", "ShareName", "Code", "DownloadCount", "FileName", "FileNames", "StartedAt", "Username", "LoginURL", "SetPasswordURL", "ExpiresDays"}

type limitedMailWriter struct{ bytes.Buffer }

func (w *limitedMailWriter) Write(p []byte) (int, error) {
	if w.Len()+len(p) > 128<<10 {
		return 0, fmt.Errorf("rendered template too large")
	}
	return w.Buffer.Write(p)
}

func executeMailTemplate(source string, data map[string]any) (string, error) {
	t, err := template.New("mail").Option("missingkey=error").Parse(source)
	if err != nil {
		return "", err
	}
	var b limitedMailWriter
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

func validateMailTemplate(source string, subject bool) error {
	limit := 64 << 10
	if subject {
		limit = 500
	}
	if len(source) > limit || !utf8.ValidString(source) || strings.ContainsRune(source, '\x00') {
		return fmt.Errorf("invalid or oversized template")
	}
	if subject && strings.ContainsAny(source, "\r\n") {
		return fmt.Errorf("subject must be one line")
	}
	t, err := template.New("mail").Parse(source)
	if err != nil {
		return err
	}
	// Restrict new edits to text and simple variables: no loops, definitions,
	// function calls or conditional branches hiding invalid variables.
	for _, node := range t.Tree.Root.Nodes {
		switch n := node.(type) {
		case *parse.TextNode:
		case *parse.ActionNode:
			if len(n.Pipe.Decl) != 0 || len(n.Pipe.Cmds) != 1 || len(n.Pipe.Cmds[0].Args) != 1 {
				return fmt.Errorf("use simple {{.Variable}} placeholders")
			}
			field, ok := n.Pipe.Cmds[0].Args[0].(*parse.FieldNode)
			if !ok || len(field.Ident) != 1 {
				return fmt.Errorf("use simple {{.Variable}} placeholders")
			}
			found := false
			for _, v := range mailVars {
				found = found || field.Ident[0] == v
			}
			if !found {
				return fmt.Errorf("unknown variable: %s", field.Ident[0])
			}
		default:
			return fmt.Errorf("use text and simple {{.Variable}} placeholders")
		}
	}
	if len(t.Templates()) != 1 {
		return fmt.Errorf("template definitions are not supported")
	}
	data := map[string]any{}
	for _, v := range mailVars {
		data[v] = "example"
	}
	_, err = executeMailTemplate(source, data)
	return err
}

func (s *Server) renderMail(event, language string, supplied map[string]any) (string, string, string, error) {
	if !s.St.ValidMailLanguage(language) {
		language = "en"
	}
	if _, _, ok := s.templateKey("tpl_" + event); !ok {
		return "", "", language, fmt.Errorf("unknown mail event")
	}
	data := map[string]any{}
	for _, v := range mailVars {
		data[v] = ""
	}
	for _, v := range mailVars {
		if value, ok := supplied[v]; ok {
			data[v] = value
		}
	}
	data["ApplicationName"] = s.St.Setting("application_name")
	render := func(prefix string, subject bool) (string, error) {
		for _, lang := range []string{language, "en"} {
			source := s.St.Setting(prefix + event + "_" + lang)
			if source == "" {
				continue
			}
			// Preserve migrated source in storage, but never execute unsafe legacy
			// control flow/recursion. Invalid legacy syntax falls back to English.
			if err := validateMailTemplate(source, subject); err != nil {
				continue
			}
			if value, err := executeMailTemplate(source, data); err == nil {
				return value, nil
			}
		}
		sub, body := store.MailDefaults(event, "en")
		if subject {
			return executeMailTemplate(sub, data)
		}
		return executeMailTemplate(body, data)
	}
	subject, err := render("subj_", true)
	if err != nil {
		return "", "", language, err
	}
	body, err := render("tpl_", false)
	return subject, body, language, err
}

func (s *Server) previewTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key      string            `json:"key"`
		Language string            `json:"language"`
		Data     map[string]string `json:"data"`
		Send     bool              `json:"send"`
	}
	if decodeMailRequest(w, r, &in) != nil {
		jerr(w, 400, "bad request")
		return
	}
	if in.Language == "" {
		in.Language = "en"
	}
	if !s.St.ValidMailLanguage(in.Language) {
		jerr(w, 400, "unsupported language")
		return
	}
	data := map[string]any{}
	for k, v := range in.Data {
		found := false
		for _, allowed := range mailVars {
			found = found || k == allowed
		}
		if !found || len(v) > 8192 {
			jerr(w, 400, "invalid preview data")
			return
		}
		data[k] = v
	}
	subject, body, language, err := s.renderMail(in.Key, in.Language, data)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	if in.Send {
		u := s.ctxUser(r)
		if u.Email == "" {
			jerr(w, 400, "set an email on your profile first")
			return
		}
		if err := s.sendMail(u.Email, subject, body); err != nil {
			jerr(w, 400, err.Error())
			return
		}
	}
	jout(w, 200, map[string]any{"subject": subject, "body": body, "language": language, "sent": in.Send})
}

func decodeMailRequest(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func (s *Server) sendTemplate(to, subject, key string, data map[string]any) {
	if err := s.deliverTemplate(to, key, data); err != nil {
		log.Printf("transactional mail delivery failed: %v", err)
	}
}

func (s *Server) deliverTemplate(to, key string, data map[string]any) error {
	// Backpressure rather than silently discarding messages above concurrency.
	s.mailSlots <- struct{}{}
	defer func() { <-s.mailSlots }()
	if to == "" {
		return fmt.Errorf("recipient required")
	}
	language, _ := data["EmailLanguage"].(string)
	subject, body, _, err := s.renderMail(strings.TrimPrefix(key, "tpl_"), s.recipientLanguage(to, language), data)
	if err != nil {
		return err
	}
	return s.sendMail(to, subject, body)
}

const downloadMailCapacity = 1000

// RunDownloadMail runs one bounded worker. Pending jobs survive process restarts;
// at-least-once SMTP delivery may duplicate mail after a crash following acceptance.
func (s *Server) RunDownloadMail(ctx context.Context) {
	s.downloadMailOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				if s.processDownloadMail(ctx) {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case <-s.downloadMailWake:
				case <-ticker.C:
				}
			}
		}()
	})
}

func (s *Server) processDownloadMail(ctx context.Context) bool {
	// Acquiring before loading ensures no queued goroutine retains request/files.
	select {
	case s.mailSlots <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	defer func() { <-s.mailSlots }()
	var id int64
	var recipient, language, source string
	var attempts int
	if err := s.St.DB.QueryRow(`SELECT id,recipient,language,data,attempts FROM download_mail_outbox
		WHERE status='pending' AND next_attempt<=? ORDER BY id LIMIT 1`, time.Now()).Scan(&id, &recipient, &language, &source, &attempts); err != nil {
		return false
	}
	data := map[string]any{}
	err := json.Unmarshal([]byte(source), &data)
	if err == nil {
		var subject, body string
		subject, body, _, err = s.renderMail("share_downloaded", language, data)
		if err == nil {
			err = s.sendMail(recipient, subject, body)
		}
	}
	if err == nil {
		if _, err := s.St.DB.Exec(`DELETE FROM download_mail_outbox WHERE id=?`, id); err != nil {
			log.Printf("remove delivered download mail: %v", err)
		}
		return true
	}
	attempts++
	status := "pending"
	if attempts >= 3 {
		status = "failed"
	}
	if _, saveErr := s.St.DB.Exec(`UPDATE download_mail_outbox SET attempts=?,status=?,last_error=?,next_attempt=? WHERE id=?`, attempts, status, err.Error(), time.Now().Add(time.Duration(attempts)*30*time.Second), id); saveErr != nil {
		log.Printf("save download mail failure: %v", saveErr)
	}
	if status == "failed" {
		// Retain the newest bounded failure records for diagnosis, not an
		// ever-growing second queue when SMTP remains unavailable.
		if _, err := s.St.DB.Exec(`DELETE FROM download_mail_outbox WHERE status='failed' AND id NOT IN
			(SELECT id FROM download_mail_outbox WHERE status='failed' ORDER BY id DESC LIMIT 1000)`); err != nil {
			log.Printf("prune download mail failures: %v", err)
		}
	}
	log.Printf("download notification %d delivery attempt %d failed: %v", id, attempts, err)
	return true
}

func (s *Server) notifyInviteCompleted(ownerID int64, guest *User, url, name string) {
	var email string
	s.St.DB.QueryRow(`SELECT email FROM users WHERE id=?`, ownerID).Scan(&email)
	s.sendTemplate(email, "Files uploaded to you", "tpl_reverse_completed", map[string]any{
		"RecipientName": "", "SenderName": nameOf(guest), "URL": url, "Message": "", "Expires": "", "ShareName": name,
	})
}

// ---------- email templates API ----------

func (s *Server) getTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := s.St.DB.Query(`SELECT key,value FROM settings WHERE grp='email_templates' ORDER BY key`)
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		out[k] = v
	}
	jout(w, 200, out)
}

func (s *Server) putTemplates(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if decodeMailRequest(w, r, &in) != nil {
		jerr(w, 400, "bad request")
		return
	}
	updates := map[string]string{}
	for k, v := range in {
		canonical, subject, ok := s.templateKey(k)
		if !ok {
			jerr(w, 400, "unknown template key: "+k)
			return
		}
		if prior, exists := updates[canonical]; exists && prior != v {
			jerr(w, 400, "conflicting English alias: "+k)
			return
		}
		if err := validateMailTemplate(v, subject); err != nil {
			jerr(w, 400, k+": "+err.Error())
			return
		}
		updates[canonical] = v
	}
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	for k, v := range updates {
		if _, err := tx.Exec(`UPDATE settings SET value=? WHERE key=? AND grp='email_templates'`, v, k); err != nil {
			jerr(w, 500, "template save failed")
			return
		}
		if strings.HasPrefix(k, "tpl_") && strings.HasSuffix(k, "_en") {
			if _, err := tx.Exec(`UPDATE settings SET value=? WHERE key=? AND grp='email_templates'`, v, strings.TrimSuffix(k, "_en")); err != nil {
				jerr(w, 500, "template save failed")
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		jerr(w, 500, "template save failed")
		return
	}
	jout(w, 200, map[string]any{"ok": true})
}

func (s *Server) testEmail(w http.ResponseWriter, r *http.Request) {
	u := s.ctxUser(r)
	if u.Email == "" {
		jerr(w, 400, "set an email on your profile first")
		return
	}
	subject, body := store.TestMailText(u.Language)
	if err := s.sendMail(u.Email, subject, body); err != nil {
		jerr(w, 400, err.Error())
		return
	}
	jout(w, 200, map[string]any{"ok": true})
}
