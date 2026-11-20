package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/mhenderik/sendora/internal/storage"
	"github.com/mhenderik/sendora/internal/storage/storagetest"
)

// memLocation registers an in-memory "S3" location and routes it to m.
func memLocation(t *testing.T, e *vfEnv, m *storagetest.Memory, bucket string) int64 {
	t.Helper()
	cfg := storage.Config{Backend: storage.KindS3, S3Bucket: bucket, S3Region: "us-east-1", S3Auth: storage.S3AuthStatic, S3AccessKey: "a", S3SecretKey: "b"}
	id, err := EnsureLocation(e.st.DB, cfg)
	if err != nil || id == LocalLocation {
		t.Fatalf("location %d %v", id, err)
	}
	open := e.st.Backends().OpenFn
	e.st.Backends().OpenFn = func(ctx context.Context, c storage.Config) (storage.Backend, error) {
		if c.Backend == storage.KindS3 && c.S3Bucket == bucket {
			return m, nil
		}
		return open(ctx, c)
	}
	return id
}

func TestEnsureLocationKeepsIdentityAndRotatesCredentials(t *testing.T) {
	e := newVFEnv(t)
	cfg := storage.Config{Backend: storage.KindS3, S3Bucket: "b1", S3Region: "eu-west-1", S3Auth: storage.S3AuthStatic, S3AccessKey: "a", S3SecretKey: "old"}
	id1, _ := EnsureLocation(e.st.DB, cfg)
	cfg.S3SecretKey = "new"
	id2, _ := EnsureLocation(e.st.DB, cfg)
	if id1 != id2 {
		t.Fatalf("credential change created a new location: %d %d", id1, id2)
	}
	got, err := LoadLocation(e.st.DB, e.dir, id1)
	if err != nil || got.S3SecretKey != "new" {
		t.Fatalf("rotated secret not stored: %+v %v", got, err)
	}
	cfg.S3Bucket = "b2"
	if id3, _ := EnsureLocation(e.st.DB, cfg); id3 == id1 {
		t.Fatal("another bucket must be another location")
	}
	if id, _ := EnsureLocation(e.st.DB, storage.Config{Backend: "local"}); id != LocalLocation {
		t.Fatal("local location must be 0")
	}
}

func TestFileObjectKeyRefusesTamperedRows(t *testing.T) {
	for _, c := range []struct {
		loc       int64
		lid, disk string
		key       string
	}{
		{0, "../etc", "/x/a", ""},
		{0, vfLongID, "", ""},
		{0, vfLongID, "/x/..", ""},
		{3, vfLongID, "", "backups/sendora-encrypted-x.db"},
		{3, vfLongID, "", "files/../probe/x"},
		{3, vfLongID, "", ""},
	} {
		if k, err := FileObjectKey(c.loc, c.lid, c.disk, c.key); err == nil {
			t.Errorf("%+v accepted as %q", c, k)
		}
	}
	if k, err := FileObjectKey(0, vfLongID, "/anywhere/1_a.txt", ""); err != nil || k != "shares/"+vfLongID+"/1_a.txt" {
		t.Fatalf("local key %q %v", k, err)
	}
}

func TestMigrateEncryptsPlainFilesAndVerifies(t *testing.T) {
	e := newVFEnv(t)
	m := storagetest.NewMemory()
	loc := memLocation(t, e, m, "mig")
	plain := make([]byte, 200_000)
	rand.Read(plain)
	idPlain, _ := e.add("1_legacy.bin", "plain", plain)
	idV2, _ := e.add("2_new.bin", "v2", []byte("PLAINTEXT-MARKER-v2-file"))
	idV1, _ := e.add("3_old.bin", "v1", []byte("v1 file content"))

	st, err := MigrateFiles(context.Background(), e.st.DB, e.dir, e.master, e.st.Backends(), loc, MigrateOptions{})
	if err != nil || st.Done != 3 || st.Failed != 0 {
		t.Fatalf("migration %+v %v", st, err)
	}
	for _, id := range []int64{idPlain, idV2, idV1} {
		var sid int64
		var key, disk string
		var enc bool
		e.st.DB.QueryRow(`SELECT storage_id,object_key,disk_path,enc FROM files WHERE id=?`, id).Scan(&sid, &key, &disk, &enc)
		if sid != loc || !storage.ValidFileKey(key) || disk != "" || !enc {
			t.Fatalf("file %d: loc=%d key=%q disk=%q enc=%v", id, sid, key, disk, enc)
		}
		raw := m.Bytes(key)
		if bytes.Contains(raw, plain[:64]) || bytes.Contains(raw, []byte("PLAINTEXT-MARKER")) {
			t.Fatalf("file %d: plaintext in the object store", id)
		}
	}
	if ents, _ := os.ReadDir(e.disk("")); len(ents) != 0 {
		t.Fatalf("local source files left: %d", len(ents))
	}
	sum, err := e.st.VerifyStoredFiles(VerifyOptions{OpenBackend: e.st.Backends().OpenFn})
	if err != nil || sum.OK != 3 || sum.Failed != 0 || sum.Orphans != 0 {
		t.Fatalf("verify after migration: %+v %v", sum, err)
	}
	// The v1 file kept its format; the plain file is now v2 with a file id.
	var encID string
	e.st.DB.QueryRow(`SELECT enc_id FROM files WHERE id=?`, idPlain).Scan(&encID)
	if len(encID) != 32 {
		t.Fatalf("plain file got no file id: %q", encID)
	}

	// Back to local: the classic layout again, still verifiable.
	st, err = MigrateFiles(context.Background(), e.st.DB, e.dir, e.master, e.st.Backends(), LocalLocation, MigrateOptions{})
	if err != nil || st.Done != 3 {
		t.Fatalf("migration back %+v %v", st, err)
	}
	if len(m.Keys()) != 0 {
		t.Fatalf("remote objects left: %v", m.Keys())
	}
	sum, err = e.st.VerifyStoredFiles(VerifyOptions{OpenBackend: e.st.Backends().OpenFn})
	if err != nil || sum.OK != 3 || sum.Failed != 0 || sum.Orphans != 0 {
		t.Fatalf("verify after migrating back: %+v %v", sum, err)
	}
}

func TestMigrateLeavesCorruptFilesInPlace(t *testing.T) {
	e := newVFEnv(t)
	m := storagetest.NewMemory()
	loc := memLocation(t, e, m, "mig2")
	id, stored := e.add("1_bad.bin", "v2", bytes.Repeat([]byte("x"), 100_000))
	stored[len(stored)-5] ^= 1
	os.WriteFile(e.disk("1_bad.bin"), stored, 0o600)
	e.add("2_good.bin", "v2", []byte("good"))
	st, err := MigrateFiles(context.Background(), e.st.DB, e.dir, e.master, e.st.Backends(), loc, MigrateOptions{DeleteDelay: 0})
	if err != nil || st.Failed != 1 || st.Done != 1 {
		t.Fatalf("migration %+v %v", st, err)
	}
	var sid int64
	e.st.DB.QueryRow(`SELECT storage_id FROM files WHERE id=?`, id).Scan(&sid)
	if sid != LocalLocation {
		t.Fatal("corrupt file was switched to the copy")
	}
	if _, err := os.Stat(e.disk("1_bad.bin")); err != nil {
		t.Fatal("corrupt source was deleted")
	}
	if len(m.Keys()) != 1 {
		t.Fatalf("copy of the corrupt file left behind: %v", m.Keys())
	}
}

func TestPendingDeletesTriggerAndRetry(t *testing.T) {
	e := newVFEnv(t)
	m := storagetest.NewMemory()
	loc := memLocation(t, e, m, "del")
	key, _ := storage.NewFileKey()
	m.Put(context.Background(), key, bytes.NewReader([]byte("x")), 1)
	if _, err := e.st.DB.Exec(`INSERT INTO files(share_id,name,full_path,size,disk_path,storage_id,object_key,enc) VALUES(1,'a','a',1,'',?,?,1)`, loc, key); err != nil {
		t.Fatal(err)
	}
	// Deleting the share cascades to the file row; the trigger queues the object.
	if _, err := e.st.DB.Exec(`DELETE FROM shares WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if n := PendingDeletes(e.st.DB); n != 1 {
		t.Fatalf("queued %d", n)
	}
	done, failed := ProcessPendingDeletes(context.Background(), e.st.DB, e.st.Backends(), 10)
	if done != 1 || failed != 0 || len(m.Keys()) != 0 || PendingDeletes(e.st.DB) != 0 {
		t.Fatalf("done=%d failed=%d keys=%v", done, failed, m.Keys())
	}
	// An unknown location fails and is retried later (not lost).
	QueueDelete(e.st.DB, 999, key, time.Now())
	done, failed = ProcessPendingDeletes(context.Background(), e.st.DB, e.st.Backends(), 10)
	if done != 0 || failed != 1 || PendingDeletes(e.st.DB) != 1 {
		t.Fatalf("retry: done=%d failed=%d", done, failed)
	}
	if done, _ = ProcessPendingDeletes(context.Background(), e.st.DB, e.st.Backends(), 10); done != 0 {
		t.Fatal("a failed deletion must wait before the next attempt")
	}
}
