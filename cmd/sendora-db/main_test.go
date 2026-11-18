package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"github.com/mhenderik/sendora/internal/filecrypt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mhenderik/sendora/internal/store"
)

func TestEncryptedBackupCommand(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{0x75}, 32)
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenEncrypted(filepath.Join(dir, "data"), key, true)
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	if _, err := st.DB.Exec(`INSERT INTO users(username) VALUES('backup-test')`); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "backup.db")
	args := []string{"backup", "-source", st.Path, "-destination", destination, "-key-file", keyFile}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	counts, err := store.VerifyDatabase(destination, key)
	if err != nil || counts["users"] != 1 {
		t.Fatal(counts, err)
	}
	if _, err := store.VerifyDatabase(destination, nil); err == nil {
		t.Fatal("unencrypted backup")
	}
	original, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(args); err == nil {
		t.Fatal("overwrote existing backup")
	}
	current, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(original, current) {
		t.Fatal("existing backup changed", err)
	}
}

func TestMFAResetCommand(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{0x42}, 32)
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenEncrypted(filepath.Join(dir, "data"), key, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users(id,username) VALUES(1,'locked'),(2,'bystander')`,
		`INSERT INTO user_totp(user_id,secret,confirmed) VALUES(1,'S',1),(2,'S',1)`,
		`INSERT INTO user_recovery_codes(user_id,code_hash) VALUES(1,'a'),(2,'b')`,
		`INSERT INTO user_webauthn(user_id,cred_id,public_key,alg) VALUES(1,x'01',x'02',-7)`,
		`INSERT INTO refresh_tokens(hash,user_id,expires_at) VALUES('h1',1,'2099-01-01'),('h2',2,'2099-01-01')`,
		`UPDATE users SET login_failures=5,login_locked_until=4102444800 WHERE id=1`,
	} {
		if _, err := st.DB.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	path := st.Path
	st.DB.Close()
	args := []string{"mfa-reset", "-source", path, "-key-file", keyFile, "-user"}
	if err := run(append(args, "nobody")); err == nil {
		t.Fatal("unknown user must fail")
	}
	if err := run(args[:len(args)-1]); err == nil {
		t.Fatal("user is required")
	}
	if err := run(append(args, "locked")); err != nil {
		t.Fatal(err)
	}
	st, err = store.OpenEncrypted(filepath.Join(dir, "data"), key, false)
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	count := func(q string) int {
		var n int
		if err := st.DB.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(`SELECT COUNT(*) FROM user_totp WHERE user_id=1`)+count(`SELECT COUNT(*) FROM user_recovery_codes WHERE user_id=1`)+count(`SELECT COUNT(*) FROM user_webauthn WHERE user_id=1`)+count(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=1`) != 0 {
		t.Fatal("the user's factors and sessions must be gone")
	}
	if count(`SELECT COUNT(*) FROM users WHERE id=1 AND login_failures=0 AND login_locked_until=0 AND mfa_epoch=1`) != 1 {
		t.Fatal("the lock is cleared and the epoch bumped")
	}
	if count(`SELECT COUNT(*) FROM user_totp WHERE user_id=2`)+count(`SELECT COUNT(*) FROM user_recovery_codes WHERE user_id=2`)+count(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=2`) != 3 {
		t.Fatal("other users must be untouched")
	}
	// Wrong key never modifies anything.
	bad := filepath.Join(dir, "badkey")
	os.WriteFile(bad, bytes.Repeat([]byte{0x43}, 32), 0600)
	if err := run([]string{"mfa-reset", "-source", path, "-key-file", bad, "-user", "bystander"}); err == nil {
		t.Fatal("wrong key must fail")
	}
}

func TestVerifyFilesCommand(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{0x33}, 32)
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	st, err := store.OpenEncrypted(data, key, true)
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	lid := "bbbbbbbbbbbbbbbb"
	if _, err := st.DB.Exec(`INSERT INTO shares(long_id,name) VALUES(?,'SHARE-NAME-X')`, lid); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(data, "shares", lid), 0700); err != nil {
		t.Fatal(err)
	}
	plain := []byte("verify me")
	var out bytes.Buffer
	_, encID, err := filecrypt.Encrypt(&out, bytes.NewReader(plain), st.FileKey(), nil)
	if err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(data, "shares", lid, "0_f.txt")
	if err := os.WriteFile(disk, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(plain)
	if _, err := st.DB.Exec(`INSERT INTO files(share_id,name,full_path,size,disk_path,sha256,enc,enc_id) VALUES(1,'f.txt','f.txt',?,?,?,1,?)`, len(plain), disk, hex.EncodeToString(sum[:]), encID); err != nil {
		t.Fatal(err)
	}
	args := []string{"verify-files", "-source", st.Path, "-key-file", keyFile, "-data-dir", data, "-json"}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	// Flip one ciphertext bit: the command must fail in both output modes.
	b := out.Bytes()
	b[len(b)-1] ^= 1
	if err := os.WriteFile(disk, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(args); err == nil {
		t.Fatal("tampered file did not fail verify-files")
	}
	if err := run([]string{"verify-files", "-source", st.Path, "-key-file", keyFile, "-data-dir", data}); err == nil {
		t.Fatal("text mode must fail as well")
	}
}
