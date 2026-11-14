package app

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Three preparations bound temporary disk usage to three times the per-response limit.
// Snapshots are private and removed after serving; no source descriptors are retained.
var integritySlots = make(chan struct{}, 3)
var errIntegrity = errors.New("file integrity check failed")

// One client address may hold at most two of the three verified-download slots:
// a person can download two files at once, while a single slow client can never
// block every download on the instance.
const downloadSlotsPerClient = 2

var (
	downloadClientsMu sync.Mutex
	downloadClients   = map[string]int{}
)

func acquireDownloadClient(ctx context.Context, client string) bool {
	deadline := time.Now().Add(5 * time.Second)
	for {
		downloadClientsMu.Lock()
		if downloadClients[client] < downloadSlotsPerClient {
			downloadClients[client]++
			downloadClientsMu.Unlock()
			return true
		}
		downloadClientsMu.Unlock()
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func releaseDownloadClient(client string) {
	downloadClientsMu.Lock()
	if downloadClients[client] <= 1 {
		delete(downloadClients, client)
	} else {
		downloadClients[client]--
	}
	downloadClientsMu.Unlock()
}

// prepareClientDownload is prepareDownload limited to one slot per client.
// On success the caller releases both: <-integritySlots and releaseDownloadClient.
func (s *Server) prepareClientDownload(ctx context.Context, client string, files []FileInfo) (*os.File, string, error) {
	if !acquireDownloadClient(ctx, client) {
		return nil, "", fmt.Errorf("verified download preparation busy")
	}
	f, sum, err := s.prepareDownload(ctx, files)
	if err != nil {
		releaseDownloadClient(client)
	}
	return f, sum, err
}

var errSnapshotLimit = errors.New("verified download exceeds preparation limit")

func validSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type boundedWriter struct {
	w         io.Writer
	remaining int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errSnapshotLimit
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func snapshotLimit() int64 {
	// A single ZIP may need slightly more disk than the total source size.
	for _, name := range []string{"SENDORA_VERIFIED_DOWNLOAD_MAX_BYTES", "FILESHARE_VERIFIED_DOWNLOAD_MAX_BYTES"} {
		if n, err := strconv.ParseInt(os.Getenv(name), 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 16 << 30
}

func hashDisk(ctx context.Context, p string, size int64) (string, error) {
	f, err := openDownloadFile(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, contextReader{ctx, io.LimitReader(f, size+1)})
	if err != nil {
		return "", err
	}
	if n != size {
		return "", errIntegrity
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyVerifiedSource verifies the bytes actually copied, eliminating the gap
// between hashing a mutable source and streaming it. Legacy files get a first
// baseline only; that cannot establish what their original uploader sent.
func (s *Server) copyVerifiedSource(ctx context.Context, dst io.Writer, f FileInfo) error {
	in, err := openDownloadFile(f.diskPath)
	if err != nil {
		return err
	}
	defer in.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), contextReader{ctx, io.LimitReader(in, f.Size+1)})
	if err != nil {
		return err
	}
	checksum := hex.EncodeToString(h.Sum(nil))
	if n != f.Size || (f.SHA256 != "" && checksum != f.SHA256) {
		return errIntegrity
	}
	if f.SHA256 == "" {
		if _, err = s.St.DB.Exec(`UPDATE files SET sha256=? WHERE id=? AND sha256=''`, checksum, f.ID); err != nil {
			return err
		}
		var baseline string
		if err = s.St.DB.QueryRow(`SELECT sha256 FROM files WHERE id=?`, f.ID).Scan(&baseline); err != nil {
			return err
		}
		if baseline != checksum {
			return errIntegrity
		}
	}
	return nil
}

// prepareDownload returns a complete, verified, private snapshot. Nothing is
// served or counted until all sources (and ZIP finalization) have succeeded.
func (s *Server) prepareDownload(ctx context.Context, files []FileInfo) (*os.File, string, error) {
	wait := time.NewTimer(5 * time.Second)
	defer wait.Stop()
	select {
	case integritySlots <- struct{}{}:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case <-wait.C:
		return nil, "", fmt.Errorf("verified download preparation busy")
	}
	retained := false
	defer func() {
		if !retained {
			<-integritySlots
		}
	}()
	limit := snapshotLimit()
	var total int64
	for _, f := range files {
		if f.Size < 0 || f.Size > limit-total {
			return nil, "", errSnapshotLimit
		}
		total += f.Size
	}
	out, err := os.CreateTemp(s.St.DataDir, ".verified-download-*")
	if err != nil {
		return nil, "", err
	}
	ok := false
	defer func() {
		if !ok {
			out.Close()
			os.Remove(out.Name())
		}
	}()
	h := sha256.New()
	writer := &boundedWriter{w: io.MultiWriter(out, h), remaining: limit}
	if len(files) == 1 {
		err = s.copyVerifiedSource(ctx, writer, files[0])
	} else {
		zw := zip.NewWriter(writer)
		for _, f := range files {
			if err = validateArchivePath(f.FullPath); err != nil {
				break
			}
			// Stable metadata; digest covers the exact archive, not only its members.
			hdr := &zip.FileHeader{Name: f.FullPath, Method: zip.Store}
			hdr.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
			var entry io.Writer
			entry, err = zw.CreateHeader(hdr)
			if err == nil {
				err = s.copyVerifiedSource(ctx, entry, f)
			}
			if err != nil {
				break
			}
		}
		if err == nil {
			err = zw.Close()
		}
	}
	if err != nil {
		return nil, "", err
	}
	if _, err = out.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	ok = true
	retained = true
	return out, hex.EncodeToString(h.Sum(nil)), nil
}

func setDownloadChecksum(w http.ResponseWriter, checksum string) {
	b, _ := hex.DecodeString(checksum)
	// Representation digest remains a full-file digest even for native Range requests.
	w.Header().Set("Repr-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(b)+":")
	w.Header().Set("X-Checksum-SHA256", checksum)
	w.Header().Set("Cache-Control", "no-store")
}

func integrityError(w http.ResponseWriter, err error) {
	if os.IsNotExist(err) {
		jerr(w, 404, "file missing")
		return
	}
	if errors.Is(err, errIntegrity) {
		jerr(w, http.StatusConflict, "file integrity check failed; download blocked")
		return
	}
	if errors.Is(err, errSnapshotLimit) {
		jerr(w, http.StatusRequestEntityTooLarge, errSnapshotLimit.Error())
		return
	}
	jerr(w, http.StatusServiceUnavailable, "verified download temporarily unavailable")
}
