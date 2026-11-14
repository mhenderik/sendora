package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhenderik/sendora/internal/webauthnlite"

	"github.com/go-chi/chi/v5"
)

// WebAuthn (passkeys / security keys) as second factor. Verification of the
// ceremonies lives in internal/webauthnlite; this file holds the HTTP wiring,
// the single-use challenge store and the credential bookkeeping.
//
// Attestation trust is deliberately not evaluated (attestation "none"): any
// authenticator may be registered, which is the usual choice for a self-hosted
// second factor.

const (
	waCeremonyTTL     = 5 * time.Minute
	waCeremonyCap     = 4096
	waMaxCredentials  = 10
	waDefaultKeyName  = "Security key"
	waMaxKeyNameRunes = 60

	msgWAFailed        = "could not verify the security key"
	msgWAExpired       = "registration expired; start again"
	msgWATooMany       = "too many security keys; remove one first"
	msgWADuplicate     = "this security key is already registered"
	msgWABusy          = "too many pending requests; try again shortly"
	waKindLogin        = "login"
	waKindRegistration = "register"
)

// waCeremonies holds pending challenges server-side. Entries are single use,
// expire after waCeremonyTTL and the map is bounded so unauthenticated callers
// cannot grow it without limit.
type waCeremonies struct {
	mu      sync.Mutex
	entries map[string]waCeremony
	ttl     time.Duration // 0 = waCeremonyTTL (tests shorten it)
	cap     int           // 0 = waCeremonyCap
}

type waCeremony struct {
	kind      string
	uid       int64
	challenge []byte
	exp       time.Time
}

func (c *waCeremonies) lifetime() time.Duration {
	if c.ttl > 0 {
		return c.ttl
	}
	return waCeremonyTTL
}

func (c *waCeremonies) limit() int {
	if c.cap > 0 {
		return c.cap
	}
	return waCeremonyCap
}

// put stores a challenge; it returns false when the store is full of live entries.
func (c *waCeremonies) put(key, kind string, uid int64, challenge []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.entries == nil {
		c.entries = map[string]waCeremony{}
	}
	if len(c.entries) >= c.limit() {
		for k, e := range c.entries {
			if !now.Before(e.exp) {
				delete(c.entries, k)
			}
		}
		if _, replacing := c.entries[key]; !replacing && len(c.entries) >= c.limit() {
			return false
		}
	}
	c.entries[key] = waCeremony{kind: kind, uid: uid, challenge: challenge, exp: now.Add(c.lifetime())}
	return true
}

// take removes and returns the challenge: every ceremony can be completed once.
func (c *waCeremonies) take(key, kind string, uid int64) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	delete(c.entries, key)
	if !time.Now().Before(e.exp) || e.kind != kind || e.uid != uid {
		return nil, false
	}
	return e.challenge, true
}

func (c *waCeremonies) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// ---------- helpers ----------

func (s *Server) waRP() (webauthnlite.RP, bool) {
	return webauthnlite.RPFromAppURL(s.mfaAppURL())
}

// requireWebAuthn answers 409 unless passkeys are switched on and usable.
func (s *Server) requireWebAuthn(w http.ResponseWriter, u *User) bool {
	if !s.requireMFAOn(w, u, false) {
		return false
	}
	if !s.mfaWebAuthnEnabled() {
		jerr(w, 409, msgMFANotEnabled)
		return false
	}
	return true
}

// waUserHandle is an opaque, stable per-account handle that reveals nothing
// about the account (WebAuthn forbids personal data in the user handle).
func (s *Server) waUserHandle(uid int64) []byte {
	m := hmac.New(sha256.New, s.Secret)
	m.Write([]byte("webauthn-user:" + strconv.FormatInt(uid, 10)))
	return m.Sum(nil)
}

type waStoredCred struct {
	rowID int64
	webauthnlite.Credential
}

func (s *Server) waLoadCredentials(uid int64) []waStoredCred {
	out := []waStoredCred{}
	rows, err := s.St.DB.Query(`SELECT id,cred_id,public_key,alg,sign_count FROM user_webauthn WHERE user_id=? ORDER BY id`, uid)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c waStoredCred
		var count int64
		if rows.Scan(&c.rowID, &c.ID, &c.PublicKey, &c.Alg, &count) == nil {
			if count < 0 || count > 0xFFFFFFFF {
				count = 0xFFFFFFFF
			}
			c.SignCount = uint32(count)
			out = append(out, c)
		}
	}
	return out
}

func credIDs(cs []waStoredCred) [][]byte {
	out := make([][]byte, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

// waBrowserCredential is the JSON the browser helper posts.
type waBrowserCredential struct {
	ID       string `json:"id"`
	RawID    string `json:"rawId"`
	Type     string `json:"type"`
	Response struct {
		ClientDataJSON    string `json:"clientDataJSON"`
		AttestationObject string `json:"attestationObject"`
		AuthenticatorData string `json:"authenticatorData"`
		Signature         string `json:"signature"`
		UserHandle        string `json:"userHandle"`
	} `json:"response"`
}

// credentialID returns the decoded id after checking that id and rawId agree.
func (c *waBrowserCredential) credentialID() ([]byte, bool) {
	if c.Type != "public-key" {
		return nil, false
	}
	id, err := webauthnlite.DecodeBase64URL(c.ID)
	if err != nil || len(id) == 0 || len(id) > 1023 {
		return nil, false
	}
	if c.RawID != "" {
		raw, err := webauthnlite.DecodeBase64URL(c.RawID)
		if err != nil || subtle.ConstantTimeCompare(raw, id) != 1 {
			return nil, false
		}
	}
	return id, true
}

func cleanKeyName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return waDefaultKeyName, true
	}
	if validateText(name, waMaxKeyNameRunes, true) != nil {
		return "", false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return name, true
}

// ---------- login ----------

// pendingMFAUser resolves and checks the pending token and the account for the
// WebAuthn login endpoints. On failure it has answered already.
func (s *Server) pendingMFAUser(w http.ResponseWriter, token string) (u *User, jti string, exp time.Time, ok bool) {
	uid, jti, cred, exp, valid := s.parseMFAToken(token)
	if !valid {
		jerr(w, 401, msgMFAExpired)
		return nil, "", time.Time{}, false
	}
	u, err := s.userByID(uid)
	if err != nil || u.IsGuest || cred != s.credentialVersion(u) {
		jerr(w, 401, msgMFAExpired)
		return nil, "", time.Time{}, false
	}
	if s.mfaGlobal() == "off" || !s.mfaWebAuthnEnabled() {
		jerr(w, 409, msgMFANotEnabled)
		return nil, "", time.Time{}, false
	}
	if u.loginLockedUntil > time.Now().Unix() {
		s.logEvent(u.ID, u.Username, "mfa_failed", "", "account locked")
		rejectMFA(w)
		return nil, "", time.Time{}, false
	}
	return u, jti, exp, true
}

// mfaWebAuthnLoginBegin: POST /api/auth/mfa/webauthn/begin {mfa_token}
func (s *Server) mfaWebAuthnLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalLogin(w) {
		return
	}
	var b struct {
		MFAToken string `json:"mfa_token"`
	}
	if decode(r, &b) != nil {
		jerr(w, 400, "bad request")
		return
	}
	uid, _, _, _, valid := s.parseMFAToken(b.MFAToken)
	if !valid {
		jerr(w, 401, msgMFAExpired)
		return
	}
	unlock := lockStripe(&s.loginLocks, strconv.FormatInt(uid, 10))
	defer unlock()
	u, jti, _, ok := s.pendingMFAUser(w, b.MFAToken)
	if !ok {
		return
	}
	rp, rpOK := s.waRP()
	creds := s.waLoadCredentials(u.ID)
	if !rpOK || len(creds) == 0 {
		jerr(w, 409, msgMFANotEnabled)
		return
	}
	challenge, err := webauthnlite.NewChallenge()
	if err != nil {
		jerr(w, 500, "could not create challenge")
		return
	}
	if !s.waCeremonies.put(jti, waKindLogin, u.ID, challenge) {
		jerr(w, 503, msgWABusy)
		return
	}
	jout(w, 200, map[string]any{"options": webauthnlite.RequestOptions(rp, challenge, credIDs(creds))})
}

// mfaWebAuthnLoginFinish: POST /api/auth/mfa/webauthn/finish {mfa_token, credential}
func (s *Server) mfaWebAuthnLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalLogin(w) {
		return
	}
	var b struct {
		MFAToken   string              `json:"mfa_token"`
		Credential waBrowserCredential `json:"credential"`
	}
	if decode(r, &b) != nil {
		jerr(w, 400, "bad request")
		return
	}
	uid, _, _, _, valid := s.parseMFAToken(b.MFAToken)
	if !valid {
		jerr(w, 401, msgMFAExpired)
		return
	}
	unlock := lockStripe(&s.loginLocks, strconv.FormatInt(uid, 10))
	defer unlock()
	u, jti, exp, ok := s.pendingMFAUser(w, b.MFAToken)
	if !ok {
		return
	}
	rp, rpOK := s.waRP()
	if !rpOK {
		jerr(w, 409, msgMFANotEnabled)
		return
	}
	challenge, found := s.waCeremonies.take(jti, waKindLogin, u.ID)
	if !found {
		// No (or an already used or expired) challenge: the client has to begin again.
		jerr(w, 401, msgMFAExpired)
		return
	}
	fail := func() {
		s.noteAuthFailure(u)
		s.logEvent(u.ID, u.Username, "mfa_failed", "", "webauthn")
		rejectMFA(w)
	}
	id, idOK := b.Credential.credentialID()
	if !idOK {
		fail()
		return
	}
	var stored *waStoredCred
	for _, c := range s.waLoadCredentials(u.ID) {
		if subtle.ConstantTimeCompare(c.ID, id) == 1 {
			c := c
			stored = &c
			break
		}
	}
	if stored == nil {
		fail()
		return
	}
	clientData, e1 := webauthnlite.DecodeBase64URL(b.Credential.Response.ClientDataJSON)
	authData, e2 := webauthnlite.DecodeBase64URL(b.Credential.Response.AuthenticatorData)
	sig, e3 := webauthnlite.DecodeBase64URL(b.Credential.Response.Signature)
	if e1 != nil || e2 != nil || e3 != nil {
		fail()
		return
	}
	newCount, err := webauthnlite.VerifyAssertion(rp, challenge, stored.Credential, clientData, authData, sig)
	if err != nil {
		// A counter regression means a cloned authenticator: same generic answer.
		fail()
		return
	}
	if !s.mfaUsed.mark(jti, exp) {
		jerr(w, 401, msgMFAExpired)
		return
	}
	s.St.DB.Exec(`UPDATE user_webauthn SET sign_count=?,last_used_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=?`, int64(newCount), stored.rowID, u.ID)
	s.completeMFALogin(w, r, u, "webauthn")
}

// ---------- registration (authenticated) ----------

// mfaWebAuthnRegisterBegin: POST /api/users/me/mfa/webauthn/begin {password}
func (s *Server) mfaWebAuthnRegisterBegin(w http.ResponseWriter, r *http.Request) {
	u := s.ctxUser(r)
	var b struct {
		Password string `json:"password"`
	}
	if decode(r, &b) != nil {
		jerr(w, 400, "bad request")
		return
	}
	if !s.requireWebAuthn(w, u) || !s.confirmMFAPassword(w, u, b.Password) {
		return
	}
	rp, ok := s.waRP()
	if !ok {
		jerr(w, 409, msgMFANotEnabled)
		return
	}
	creds := s.waLoadCredentials(u.ID)
	if len(creds) >= waMaxCredentials {
		jerr(w, 409, msgWATooMany)
		return
	}
	challenge, err := webauthnlite.NewChallenge()
	if err != nil {
		jerr(w, 500, "could not create challenge")
		return
	}
	challengeID := randHex(16)
	if !s.waCeremonies.put(challengeID, waKindRegistration, u.ID, challenge) {
		jerr(w, 503, msgWABusy)
		return
	}
	display := u.Name
	if display == "" {
		display = u.Username
	}
	rpName := s.St.Setting("application_name")
	if rpName == "" {
		rpName = "Sendora"
	}
	opts := webauthnlite.CreationOptions(rp, rpName, webauthnlite.UserEntity{ID: s.waUserHandle(u.ID), Name: u.Username, DisplayName: display}, challenge, credIDs(creds))
	jout(w, 200, map[string]any{"challenge_id": challengeID, "options": opts})
}

// mfaWebAuthnRegisterFinish: POST /api/users/me/mfa/webauthn/finish
func (s *Server) mfaWebAuthnRegisterFinish(w http.ResponseWriter, r *http.Request) {
	u := s.ctxUser(r)
	var b struct {
		ChallengeID string              `json:"challenge_id"`
		Name        string              `json:"name"`
		Credential  waBrowserCredential `json:"credential"`
	}
	if decode(r, &b) != nil {
		jerr(w, 400, "bad request")
		return
	}
	if !s.requireWebAuthn(w, u) {
		return
	}
	rp, ok := s.waRP()
	if !ok {
		jerr(w, 409, msgMFANotEnabled)
		return
	}
	name, nameOK := cleanKeyName(b.Name)
	challenge, found := s.waCeremonies.take(b.ChallengeID, waKindRegistration, u.ID)
	if !found {
		jerr(w, 400, msgWAExpired)
		return
	}
	if !nameOK {
		jerr(w, 400, "invalid name")
		return
	}
	id, idOK := b.Credential.credentialID()
	clientData, e1 := webauthnlite.DecodeBase64URL(b.Credential.Response.ClientDataJSON)
	attestation, e2 := webauthnlite.DecodeBase64URL(b.Credential.Response.AttestationObject)
	if !idOK || e1 != nil || e2 != nil {
		s.logEvent(u.ID, u.Username, "mfa_failed", "", "webauthn registration")
		jerr(w, 400, msgWAFailed)
		return
	}
	cred, err := webauthnlite.VerifyRegistration(rp, challenge, clientData, attestation)
	if err != nil || subtle.ConstantTimeCompare(cred.ID, id) != 1 {
		s.logEvent(u.ID, u.Username, "mfa_failed", "", "webauthn registration")
		jerr(w, 400, msgWAFailed)
		return
	}
	hadCodes := s.loadFactors(u.ID).Recovery > 0
	if len(s.waLoadCredentials(u.ID)) >= waMaxCredentials {
		jerr(w, 409, msgWATooMany)
		return
	}
	if _, err := s.St.DB.Exec(`INSERT INTO user_webauthn(user_id,cred_id,public_key,alg,sign_count,name) VALUES(?,?,?,?,?,?)`,
		u.ID, cred.ID, cred.PublicKey, cred.Alg, int64(cred.SignCount), name); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			jerr(w, 409, msgWADuplicate)
			return
		}
		jerr(w, 500, "could not save")
		return
	}
	extra := map[string]any{}
	if !hadCodes {
		codes, err := s.regenerateRecoveryCodes(u.ID)
		if err != nil {
			jerr(w, 500, "could not create recovery codes")
			return
		}
		extra["recovery_codes"] = codes
	}
	s.logEvent(u.ID, u.Username, "mfa_enrolled", "", "webauthn")
	s.mfaFactorsChanged(w, r, u.ID, extra)
}

func waRowID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id, err == nil && id > 0
}

// mfaWebAuthnRename: PUT /api/users/me/mfa/webauthn/{id} {name}
func (s *Server) mfaWebAuthnRename(w http.ResponseWriter, r *http.Request) {
	u := s.ctxUser(r)
	var b struct {
		Name string `json:"name"`
	}
	if decode(r, &b) != nil {
		jerr(w, 400, "bad request")
		return
	}
	if !s.requireWebAuthn(w, u) {
		return
	}
	id, ok := waRowID(r)
	name, nameOK := cleanKeyName(b.Name)
	if !nameOK {
		jerr(w, 400, "invalid name")
		return
	}
	if !ok {
		jerr(w, 404, "not found")
		return
	}
	res, err := s.St.DB.Exec(`UPDATE user_webauthn SET name=? WHERE id=? AND user_id=?`, name, id, u.ID)
	if err != nil {
		jerr(w, 500, "could not save")
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		jerr(w, 404, "not found")
		return
	}
	jout(w, 200, map[string]any{"ok": true})
}

// mfaWebAuthnRemove: POST /api/users/me/mfa/webauthn/{id}/remove {password}
func (s *Server) mfaWebAuthnRemove(w http.ResponseWriter, r *http.Request) {
	u := s.ctxUser(r)
	var b struct {
		Password string `json:"password"`
	}
	if decode(r, &b) != nil {
		jerr(w, 400, "bad request")
		return
	}
	if !s.requireWebAuthn(w, u) || !s.confirmMFAPassword(w, u, b.Password) {
		return
	}
	id, ok := waRowID(r)
	if !ok {
		jerr(w, 404, "not found")
		return
	}
	var owner int64
	if err := s.St.DB.QueryRow(`SELECT user_id FROM user_webauthn WHERE id=?`, id).Scan(&owner); err != nil || owner != u.ID {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			jerr(w, 500, "database error")
			return
		}
		jerr(w, 404, "not found")
		return
	}
	f := s.loadFactors(u.ID)
	otherKeys := f.WebAuthn - 1
	if s.mfaRequired(u) && !((s.mfaTOTPEnabled() && f.TOTP) || (s.mfaWebAuthnEnabled() && otherKeys > 0)) {
		jerr(w, 409, msgMFARequiredKeep)
		return
	}
	if _, err := s.St.DB.Exec(`DELETE FROM user_webauthn WHERE id=? AND user_id=?`, id, u.ID); err != nil {
		jerr(w, 500, "could not remove")
		return
	}
	if !f.TOTP && otherKeys <= 0 {
		s.St.DB.Exec(`DELETE FROM user_recovery_codes WHERE user_id=?`, u.ID)
	}
	s.logEvent(u.ID, u.Username, "mfa_removed", "", "webauthn")
	s.mfaFactorsChanged(w, r, u.ID, nil)
}
