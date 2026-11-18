package store

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// FileKey is the master key for encrypting stored share files, derived from the
// database key. It is nil for the plaintext fixture store, which then stores
// share files unencrypted. Callers must not modify the slice.
func (s *Store) FileKey() []byte { return s.fileKey }

// KeyFingerprint identifies a database key without revealing it: the first 8
// bytes (hex) of HMAC-SHA256(key, "sendora key fingerprint v1"). Operators
// compare it to confirm the same key is in use; it is safe to log.
func KeyFingerprint(key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("sendora key fingerprint v1"))
	return hex.EncodeToString(m.Sum(nil)[:8])
}

// KeyFingerprint is the fingerprint of this store's key ("none" for the plaintext
// fixture store).
func (s *Store) KeyFingerprint() string {
	if !s.IsEncrypted() {
		return "none"
	}
	return KeyFingerprint(s.cipherKey)
}

// CipherVersion is the SQLCipher version string reported by the engine.
func (s *Store) CipherVersion() string {
	var v string
	if s.DB.QueryRow(`PRAGMA cipher_version`).Scan(&v) != nil {
		return ""
	}
	return v
}

// EncryptionStatus counts stored share files by encryption state.
type EncryptionStatus struct {
	Encrypted int64 `json:"encrypted"` // files.enc = 1
	Plain     int64 `json:"plain"`     // files.enc = 0 (legacy, uploaded before 0.1.2)
}

func (s *Store) EncryptionStatus() (EncryptionStatus, error) {
	var st EncryptionStatus
	err := s.DB.QueryRow(`SELECT COALESCE(SUM(enc=1),0), COALESCE(SUM(enc=0),0) FROM files`).Scan(&st.Encrypted, &st.Plain)
	return st, err
}
