package dbcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhenderik/sendora/internal/filecrypt"
	"github.com/mhenderik/sendora/internal/storage"
	"github.com/mhenderik/sendora/internal/store"
)

func storageFixture(t *testing.T) (data, keyFile string, st *store.Store) {
	t.Helper()
	dir := t.TempDir()
	key := bytes.Repeat([]byte{0x44}, 32)
	keyFile = filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	data = filepath.Join(dir, "data")
	st, err := store.OpenEncrypted(data, key, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })
	lid := "cccccccccccccccc"
	st.DB.Exec(`INSERT INTO shares(long_id,name) VALUES(?,'S')`, lid)
	os.MkdirAll(filepath.Join(data, "shares", lid), 0700)
	plain := []byte("migrate me from the command line")
	var out bytes.Buffer
	_, encID, err := filecrypt.Encrypt(&out, bytes.NewReader(plain), st.FileKey(), nil)
	if err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(data, "shares", lid, "0_f.txt")
	os.WriteFile(disk, out.Bytes(), 0600)
	sum := sha256.Sum256(plain)
	if _, err := st.DB.Exec(`INSERT INTO files(share_id,name,full_path,size,disk_path,sha256,enc,enc_id) VALUES(1,'f.txt','f.txt',?,?,?,1,?)`, len(plain), disk, hex.EncodeToString(sum[:]), encID); err != nil {
		t.Fatal(err)
	}
	return data, keyFile, st
}

func TestStorageMigrateDryRunAndChecks(t *testing.T) {
	data, keyFile, st := storageFixture(t)
	var out bytes.Buffer
	stdout = &out
	t.Cleanup(func() { stdout = os.Stdout })
	// Local is the target: nothing to do.
	if err := run([]string{"storage-migrate", "-source", st.Path, "-key-file", keyFile}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "0 file(s) to migrate") {
		t.Fatalf("output %q", out.String())
	}
	// An S3 target from the environment (as in the server's ConfigMap): dry run only.
	t.Setenv("SENDORA_STORAGE_BACKEND", "s3")
	t.Setenv("SENDORA_STORAGE_S3_BUCKET", "dry-run-bucket")
	t.Setenv("SENDORA_STORAGE_S3_AUTH", "default")
	out.Reset()
	if err := run([]string{"storage-migrate", "-source", st.Path, "-key-file", keyFile, "-dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 file(s) to migrate") || !strings.Contains(out.String(), "dry-run-bucket") {
		t.Fatalf("output %q", out.String())
	}
	// An invalid configuration is refused before anything happens.
	t.Setenv("SENDORA_STORAGE_S3_BUCKET", "")
	if err := run([]string{"storage-migrate", "-source", st.Path, "-key-file", keyFile}); err == nil {
		t.Fatal("missing bucket accepted")
	}
	// -source must be the database of -data-dir.
	if err := run([]string{"storage-migrate", "-source", st.Path, "-key-file", keyFile, "-data-dir", t.TempDir()}); err == nil {
		t.Fatal("mismatched data directory accepted")
	}
	_ = data
}

// TestStorageMigrateToMinIO runs the whole command against MinIO when the
// emulator variables are set (CI job "storage").
func TestStorageMigrateToMinIO(t *testing.T) {
	ep := os.Getenv("SENDORA_TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("SENDORA_TEST_S3_ENDPOINT not set")
	}
	data, keyFile, st := storageFixture(t)
	stdout = &bytes.Buffer{}
	t.Cleanup(func() { stdout = os.Stdout })
	bucket := os.Getenv("SENDORA_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "sendora-test"
	}
	prefix := "cli-" + hex.EncodeToString([]byte(t.Name()))[:12] + "/"
	env := map[string]string{"SENDORA_STORAGE_BACKEND": "s3", "SENDORA_STORAGE_S3_ENDPOINT": ep, "SENDORA_STORAGE_S3_BUCKET": bucket,
		"SENDORA_STORAGE_S3_PATH_STYLE": "true", "SENDORA_STORAGE_S3_PREFIX": prefix, "SENDORA_STORAGE_S3_AUTH": "static",
		"SENDORA_STORAGE_S3_ACCESS_KEY": os.Getenv("SENDORA_TEST_S3_ACCESS_KEY"), "SENDORA_STORAGE_S3_SECRET_KEY": os.Getenv("SENDORA_TEST_S3_SECRET_KEY")}
	for k, v := range env {
		t.Setenv(k, v)
	}
	cfg := storage.Config{Backend: storage.KindS3, S3Endpoint: ep, S3Region: "us-east-1", S3Bucket: bucket, S3PathStyle: true, Prefix: prefix,
		S3Auth: storage.S3AuthStatic, S3AccessKey: env["SENDORA_STORAGE_S3_ACCESS_KEY"], S3SecretKey: env["SENDORA_STORAGE_S3_SECRET_KEY"]}
	b, err := storage.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	storage.CreateBucket(context.Background(), b)
	t.Cleanup(func() { storage.DeletePrefix(context.Background(), b, "") })
	if err := run([]string{"storage-migrate", "-source", st.Path, "-key-file", keyFile}); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(filepath.Join(data, "shares", "cccccccccccccccc")); len(ents) != 0 {
		t.Fatal("local source left behind")
	}
	n := 0
	b.List(context.Background(), storage.FilePrefix, func(storage.Info) error { n++; return nil })
	if n != 1 {
		t.Fatalf("%d objects in MinIO", n)
	}
	// verify-files reads the file back from MinIO.
	if err := run([]string{"verify-files", "-source", st.Path, "-key-file", keyFile, "-data-dir", data}); err != nil {
		t.Fatal(err)
	}
	var actions int
	st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('storage_migration_started','storage_migration_finished') AND actor_name='sendora-db'`).Scan(&actions)
	if actions != 2 {
		t.Fatalf("audit records: %d", actions)
	}
}
