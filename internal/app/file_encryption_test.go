package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mhenderik/sendora/internal/filecrypt"
)

func TestStageUploadFileEncryptsAndReadsBack(t *testing.T) {
	dir := t.TempDir()
	raw := make([]byte, 3*filecrypt.ChunkSize+99)
	rand.Read(raw)
	src := filepath.Join(dir, "0.part")
	if err := os.WriteFile(src, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	k := make([]byte, 32)
	rand.Read(k)
	master, err := filecrypt.MasterKey(k)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "final")
	sum, enc, err := stageUploadFile(context.Background(), src, dest, int64(len(raw)), master)
	if err != nil || !enc {
		t.Fatalf("stage: enc=%v err=%v", enc, err)
	}
	want := sha256.Sum256(raw)
	if sum != hex.EncodeToString(want[:]) {
		t.Fatal("checksum is not of the plaintext")
	}
	stored, _ := os.ReadFile(dest)
	if bytes.Contains(stored, raw[:64]) {
		t.Fatal("plaintext found on disk")
	}
	f, _ := os.Open(dest)
	st, _ := f.Stat()
	r, err := filecrypt.NewReader(f, st.Size(), int64(len(raw)), master)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("read back failed: %v", err)
	}
}

func TestStageUploadFilePlainWithoutKeyAndEmptyFile(t *testing.T) {
	dir := t.TempDir()
	sum, enc, err := stageUploadFile(context.Background(), filepath.Join(dir, "missing.part"), filepath.Join(dir, "empty"), 0, nil)
	if err != nil || enc {
		t.Fatalf("empty plain: enc=%v err=%v", enc, err)
	}
	if want := sha256.Sum256(nil); sum != hex.EncodeToString(want[:]) {
		t.Fatal("empty checksum wrong")
	}
	k := make([]byte, 32)
	master, _ := filecrypt.MasterKey(k)
	if _, enc, err = stageUploadFile(context.Background(), filepath.Join(dir, "missing.part"), filepath.Join(dir, "empty2"), 0, master); err != nil || !enc {
		t.Fatalf("empty encrypted: enc=%v err=%v", enc, err)
	}
	if _, _, err = stageUploadFile(context.Background(), filepath.Join(dir, "missing.part"), filepath.Join(dir, "x"), 5, nil); err == nil {
		t.Fatal("missing source with size>0 must fail")
	}
}
