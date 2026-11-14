package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	githubep "golang.org/x/oauth2/github"
)

type Provider struct {
	ID                int64  `json:"id"`
	UUID              string `json:"uuid"`
	Name              string `json:"name"`
	Type              string `json:"type"`                // oidc | google | github | entra
	TenantID          string `json:"tenant_id,omitempty"` // Entra tenant, stored in canonical issuer_url
	Enabled           bool   `json:"enabled"`
	ClientID          string `json:"client_id"`
	ClientSecret      string `json:"client_secret,omitempty"`
	SecretSet         bool   `json:"client_secret_set"`
	IssuerURL         string `json:"issuer_url"`
	Icon              string `json:"icon"`
	TrustEmail        bool   `json:"trust_email"`
	AllowRegistration bool   `json:"allow_registration"`
	CallbackURL       string `json:"callback_url,omitempty"`
}

const provCols = `id,uuid,name,type,enabled,client_id,client_secret,issuer_url,icon,trust_email,allow_registration`

func scanProvider(row interface{ Scan(...any) error }) (*Provider, error) {
	p := &Provider{}
	err := row.Scan(&p.ID, &p.UUID, &p.Name, &p.Type, &p.Enabled, &p.ClientID, &p.ClientSecret, &p.IssuerURL, &p.Icon, &p.TrustEmail, &p.AllowRegistration)
	p.SecretSet = p.ClientSecret != ""
	if p.Type == "entra" {
		p.TenantID, _ = entraTenant(p.IssuerURL)
	}
	return p, err
}

func (s *Server) providerByUUID(id string) (*Provider, error) {
	return scanProvider(s.St.DB.QueryRow(`SELECT `+provCols+` FROM auth_providers WHERE uuid=?`, id))
}

func (s *Server) listPublicProviders(w http.ResponseWriter, r *http.Request) {
	rows, _ := s.St.DB.Query(`SELECT ` + provCols + ` FROM auth_providers WHERE enabled=1 ORDER BY sort,name`)
	out := []map[string]any{}
	for rows != nil && rows.Next() {
		p, _ := scanProvider(rows)
		out = append(out, map[string]any{"uuid": p.UUID, "name": p.Name, "type": p.Type, "icon": p.Icon})
	}
	if rows != nil {
		rows.Close()
	}
	jout(w, 200, out)
}

func (s *Server) adminProviders(w http.ResponseWriter, r *http.Request) {
	rows, _ := s.St.DB.Query(`SELECT ` + provCols + ` FROM auth_providers ORDER BY sort,name`)
	out := []*Provider{}
	for rows != nil && rows.Next() {
		p, _ := scanProvider(rows)
		p.ClientSecret = ""
		out = append(out, p)
	}
	if rows != nil {
		rows.Close() // single DB connection: finish the query before running others
	}
	base := s.baseURL(r)
	for _, p := range out {
		p.CallbackURL = base + "/api/auth/oidc/" + p.UUID + "/callback"
	}
	jout(w, 200, out)
}

func validProvider(p *Provider) string {
	switch p.Type {
	case "oidc":
		if u, err := url.Parse(p.IssuerURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return "valid issuer URL required"
		}
	case "entra":
		if msg := normalizeEntra(p); msg != "" {
			return msg
		}
	case "google", "github":
	default:
		return "type must be oidc, google, github or entra"
	}
	if strings.TrimSpace(p.Name) == "" || p.ClientID == "" {
		return "name and client id required"
	}
	return ""
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request) {
	var p Provider
	if decode(r, &p) != nil {
		jerr(w, 400, "bad request")
		return
	}
	if m := validProvider(&p); m != "" {
		jerr(w, 400, m)
		return
	}
	p.UUID = uuid.NewString()
	res, err := s.St.DB.Exec(`INSERT INTO auth_providers(uuid,name,type,enabled,client_id,client_secret,issuer_url,icon,trust_email,allow_registration) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		p.UUID, p.Name, p.Type, p.Enabled, p.ClientID, p.ClientSecret, p.IssuerURL, p.Icon, p.TrustEmail, p.AllowRegistration)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	jout(w, 201, map[string]any{"id": id, "uuid": p.UUID, "callback_url": s.baseURL(r) + "/api/auth/oidc/" + p.UUID + "/callback"})
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var p Provider
	if decode(r, &p) != nil {
		jerr(w, 400, "bad request")
		return
	}
	if m := validProvider(&p); m != "" {
		jerr(w, 400, m)
		return
	}
	if !p.Enabled && s.refuseLastExternalAdmin(w, 0, id) {
		return
	}
	tx, err := s.St.DB.Begin()
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	var oldType, oldClient, oldIssuer string
	if err = tx.QueryRow(`SELECT type,client_id,issuer_url FROM auth_providers WHERE id=?`, id).Scan(&oldType, &oldClient, &oldIssuer); err != nil {
		jerr(w, 404, "provider not found")
		return
	}
	var linked int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM user_auth_providers WHERE provider_id=?`, id).Scan(&linked); err != nil {
		jerr(w, 500, "database error")
		return
	}
	if linked > 0 && (oldType != p.Type || oldClient != p.ClientID || oldIssuer != p.IssuerURL) {
		jerr(w, 409, "linked provider identity cannot change; create a new provider")
		return
	}
	_, err = tx.Exec(`UPDATE auth_providers SET name=?,type=?,enabled=?,client_id=?,issuer_url=?,icon=?,trust_email=?,allow_registration=? WHERE id=?`,
		p.Name, p.Type, p.Enabled, p.ClientID, p.IssuerURL, p.Icon, p.TrustEmail, p.AllowRegistration, id)
	if err == nil && p.ClientSecret != "" {
		_, err = tx.Exec(`UPDATE auth_providers SET client_secret=? WHERE id=?`, p.ClientSecret, id)
	}
	if err != nil {
		jerr(w, 500, "database error")
		return
	}
	if err = tx.Commit(); err != nil {
		jerr(w, 500, "database error")
		return
	}
	jout(w, 200, map[string]any{"ok": true})
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if s.refuseLastExternalAdmin(w, 0, id) {
		return
	}
	s.St.DB.Exec(`DELETE FROM auth_providers WHERE id=?`, id)
	jout(w, 200, map[string]any{"ok": true})
}

// ---------- OAuth / OIDC flow ----------

func (s *Server) oauthConfig(ctx context.Context, p *Provider, r *http.Request) (*oauth2.Config, *oidc.Provider, error) {
	cfg := &oauth2.Config{ClientID: p.ClientID, ClientSecret: p.ClientSecret,
		RedirectURL: ""}
	base, ok := s.emailBaseURL(r)
	if !ok {
		return nil, nil, fmt.Errorf("configure a trusted application URL")
	}
	cfg.RedirectURL = base + "/api/auth/oidc/" + p.UUID + "/callback"
	switch p.Type {
	case "github":
		cfg.Endpoint = githubep.Endpoint
		cfg.Scopes = []string{"read:user", "user:email"}
		return cfg, nil, nil
	}
	issuer := p.IssuerURL
	if p.Type == "google" {
		issuer = "https://accounts.google.com"
	}
	op, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, nil, err
	}
	cfg.Endpoint = op.Endpoint()
	cfg.Scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	return cfg, op, nil
}

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	p, err := s.providerByUUID(chi.URLParam(r, "uuid"))
	if err != nil || !p.Enabled {
		jerr(w, 404, "provider not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cfg, _, err := s.oauthConfig(ctx, p, r)
	if err != nil {
		jerr(w, 502, "provider unreachable")
		return
	}
	state, nonce := p.UUID+":"+randHex(16), randHex(16)
	verifier := oauth2.GenerateVerifier()
	secure := s.secureCookies(r)
	for k, v := range map[string]string{"oidc_state": state, "oidc_nonce": nonce, "oidc_pkce": verifier} {
		http.SetCookie(w, &http.Cookie{Name: k, Value: v, Path: "/api/auth/oidc", MaxAge: 600, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if p.Type != "github" {
		opts = append(opts, oidc.Nonce(nonce))
	}
	http.Redirect(w, r, cfg.AuthCodeURL(state, opts...), http.StatusFound)
}

type extIdentity struct {
	Sub, Email, Name, Username string
	EmailVerified              bool
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) { http.Redirect(w, r, "/?auth_error="+url.QueryEscape(msg), http.StatusFound) }
	p, err := s.providerByUUID(chi.URLParam(r, "uuid"))
	if err != nil || !p.Enabled {
		fail("provider not found")
		return
	}
	sc, err1 := r.Cookie("oidc_state")
	nc, err2 := r.Cookie("oidc_nonce")
	pc, err3 := r.Cookie("oidc_pkce")
	if err1 != nil || err2 != nil || err3 != nil || len(pc.Value) < 43 || len(pc.Value) > 128 || nc.Value == "" || sc.Value == "" || sc.Value != r.URL.Query().Get("state") || !strings.HasPrefix(sc.Value, p.UUID+":") {
		fail("invalid state")
		return
	}
	for _, name := range []string{"oidc_state", "oidc_nonce", "oidc_pkce"} {
		http.SetCookie(w, &http.Cookie{Name: name, Path: "/api/auth/oidc", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies(r), SameSite: http.SameSiteLaxMode})
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cfg, op, err := s.oauthConfig(ctx, p, r)
	if err != nil {
		fail("provider unreachable")
		return
	}
	tok, err := cfg.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(pc.Value))
	if err != nil {
		s.logEvent(0, "unknown", "login_failed", "", "provider "+p.Name)
		fail("login failed")
		return
	}
	var id extIdentity
	if p.Type == "github" {
		id, err = githubIdentity(ctx, cfg, tok)
	} else {
		raw, _ := tok.Extra("id_token").(string)
		var idt *oidc.IDToken
		idt, err = op.Verifier(&oidc.Config{ClientID: p.ClientID}).Verify(ctx, raw)
		if err == nil && idt.Nonce != nc.Value {
			err = fmt.Errorf("nonce mismatch")
		}
		if err == nil {
			id, err = oidcIdentity(p, idt)
		}
	}
	if err != nil || id.Sub == "" {
		s.logEvent(0, "unknown", "login_failed", "", "provider "+p.Name)
		fail("login failed")
		return
	}
	u, msg := s.resolveExternalUser(p, id)
	if u == nil {
		s.logEvent(0, "unknown", "login_failed", "", "provider "+p.Name)
		fail(msg)
		return
	}
	if !s.issueSession(w, r, u) {
		fail("session changed; log in again")
		return
	}
	s.logEvent(u.ID, u.Username, "login_ok", "", "provider "+p.Name)
	http.Redirect(w, r, "/?oidc=1", http.StatusFound)
}

func githubIdentity(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (extIdentity, error) {
	c := cfg.Client(ctx, tok)
	var me struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	resp, err := c.Get("https://api.github.com/user")
	if err != nil {
		return extIdentity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return extIdentity{}, fmt.Errorf("identity request failed")
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&me); err != nil || me.ID <= 0 {
		return extIdentity{}, fmt.Errorf("invalid identity")
	}
	id := extIdentity{Sub: strconv.FormatInt(me.ID, 10), Name: me.Name, Username: me.Login}
	resp, err = c.Get("https://api.github.com/user/emails")
	if err == nil {
		var emails []struct {
			Email    string
			Primary  bool
			Verified bool
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return id, nil
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&emails); err != nil {
			return id, nil
		}
		for _, e := range emails {
			if e.Primary {
				id.Email, id.EmailVerified = e.Email, e.Verified
			}
		}
	}
	return id, nil
}

var nonUser = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func (s *Server) resolveExternalUser(p *Provider, id extIdentity) (*User, string) {
	unlock := lockStripe(&s.identityLocks, "external-identities")
	defer unlock()
	var uid int64
	err := s.St.DB.QueryRow(`SELECT user_id FROM user_auth_providers WHERE provider_id=? AND provider_user_id=?`, p.ID, id.Sub).Scan(&uid)
	if err == nil {
		u, err := s.userByID(uid)
		if err != nil || u.IsGuest {
			return nil, "account unavailable"
		}
		return u, ""
	}
	if err != sql.ErrNoRows {
		return nil, "account unavailable"
	}
	// Provider subject links are explicit identities. Email alone must never create
	// a link: locally supplied emails are not proof of ownership.
	email := ""
	if p.Type != "entra" && p.TrustEmail && id.EmailVerified {
		var err error
		email, err = canonicalEmail(id.Email)
		if err != nil {
			return nil, "invalid provider email"
		}
	}
	if email != "" && !s.emailAvailable(email, 0) {
		return nil, "email already has an account; use its existing login"
	}
	if !p.AllowRegistration {
		return nil, "no account linked to this login"
	}
	base := nonUser.ReplaceAllString(firstNonEmpty(id.Username, strings.Split(id.Email, "@")[0], "user"), "")
	if base == "" {
		base = "user"
	}
	uname := base
	for i := 2; ; i++ {
		var n int
		s.St.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE username=? COLLATE NOCASE`, uname).Scan(&n)
		if n == 0 {
			break
		}
		uname = base + strconv.Itoa(i)
	}
	tx, err := s.St.DB.Begin()
	if err != nil {
		return nil, "could not create account"
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO users(username,name,email,password_hash) VALUES(?,?,?, '')`, uname, id.Name, email)
	if err != nil {
		return nil, "could not create account"
	}
	nid, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO user_auth_providers(user_id,provider_id,provider_user_id) VALUES(?,?,?)`, nid, p.ID, id.Sub); err != nil {
		return nil, "could not link account"
	}
	u, err := scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, nid))
	if err != nil {
		return nil, "account unavailable"
	}
	if err := tx.Commit(); err != nil {
		return nil, "could not create account"
	}
	return u, ""
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
