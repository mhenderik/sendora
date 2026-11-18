package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/mhenderik/sendora/internal/filecrypt"
)

// File check statuses reported by VerifyFiles.
const (
	FileOK          = "ok"
	FilePlainLegacy = "plain-legacy"
	FileBadHeader   = "bad-header"
	FileSizeMiss    = "size-mismatch"
	FileIDMiss      = "id-mismatch"
	FileSHAMiss     = "sha-mismatch"
	FileMissing     = "missing"
	// FileTagFail is followed by "@chunkN" in the report.
	FileTagFail = "tag-fail"
)

// FileCheck is the verdict for one files row. It never carries keys, plaintext
// or crypto error text; names appear only when the caller asked for them.
type FileCheck struct {
	ID       int64  `json:"id"`
	PathHash string `json:"path_hash"`
	Enc      bool   `json:"enc"`
	Status   string `json:"status"`
	Name     string `json:"name,omitempty"`
	Share    string `json:"share,omitempty"`
}

// OrphanFile is a file under shares/ that no files row refers to.
type OrphanFile struct {
	PathHash string `json:"path_hash"`
	Name     string `json:"name,omitempty"`
}

type FilesSummary struct {
	Rows      int `json:"rows"`
	Encrypted int `json:"encrypted"`
	Plain     int `json:"plain"`
	OK        int `json:"ok"`
	Legacy    int `json:"plain_legacy"`
	Failed    int `json:"failed"`
	Orphans   int `json:"orphans"`
}

type FilesReport struct {
	Files   []FileCheck  `json:"files"`
	Orphans []OrphanFile `json:"orphans"`
	Summary FilesSummary `json:"summary"`
}

// Failures is the number of rows with a failing status plus orphans.
func (r *FilesReport) Failures() int { return r.Summary.Failed + r.Summary.Orphans }

func pathHash(dataDir, p string) string {
	rel, err := filepath.Rel(dataDir, p)
	if err != nil {
		rel = p
	}
	h := sha256.Sum256([]byte(filepath.ToSlash(rel)))
	return hex.EncodeToString(h[:4])
}

type fileRow struct {
	id             int64
	name, fullPath string
	size           int64
	disk, sha      string
	enc            bool
	encID          string
	lid, share     string
}

// VerifyFiles checks every stored share file of the database at source against
// its row: header, stored size, file id, every chunk tag and the plaintext
// SHA-256. It also lists files on disk that no row refers to. It reads only.
func VerifyFiles(source, dataDir string, key []byte, names bool) (*FilesReport, error) {
	master, err := filecrypt.MasterKey(key)
	if err != nil {
		return nil, err
	}
	db, err := openCipherDB(source, key, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT f.id,f.name,f.full_path,f.size,f.disk_path,f.sha256,f.enc,f.enc_id,s.long_id,s.name FROM files f JOIN shares s ON s.id=f.share_id ORDER BY f.id`)
	if err != nil {
		return nil, errors.New("files table cannot be read")
	}
	var list []fileRow
	for rows.Next() {
		var r fileRow
		var enc int
		if err := rows.Scan(&r.id, &r.name, &r.fullPath, &r.size, &r.disk, &r.sha, &enc, &r.encID, &r.lid, &r.share); err != nil {
			rows.Close()
			return nil, errors.New("files table cannot be read")
		}
		r.enc = enc == 1
		list = append(list, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, errors.New("files table cannot be read")
	}

	rep := &FilesReport{Files: []FileCheck{}, Orphans: []OrphanFile{}}
	known := map[string]bool{}
	for _, r := range list {
		c := FileCheck{ID: r.id, Enc: r.enc}
		if names {
			c.Name, c.Share = r.fullPath, r.share
		}
		p, perr := SharePath(dataDir, r.lid, r.disk)
		if perr != nil {
			c.PathHash, c.Status = "00000000", FileMissing
		} else {
			known[p] = true
			c.PathHash = pathHash(dataDir, p)
			c.Status = checkFile(p, r, master)
		}
		rep.Files = append(rep.Files, c)
		rep.Summary.Rows++
		if r.enc {
			rep.Summary.Encrypted++
		} else {
			rep.Summary.Plain++
		}
		switch c.Status {
		case FileOK:
			rep.Summary.OK++
		case FilePlainLegacy:
			rep.Summary.Legacy++
		default:
			rep.Summary.Failed++
		}
	}

	shares := filepath.Join(dataDir, "shares")
	entries, _ := os.ReadDir(shares)
	for _, d := range entries {
		if !d.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(shares, d.Name()))
		for _, f := range files {
			p := filepath.Join(shares, d.Name(), f.Name())
			if known[p] {
				continue
			}
			o := OrphanFile{PathHash: pathHash(dataDir, p)}
			if names {
				o.Name = f.Name()
			}
			rep.Orphans = append(rep.Orphans, o)
		}
	}
	sort.Slice(rep.Orphans, func(i, j int) bool { return rep.Orphans[i].PathHash < rep.Orphans[j].PathHash })
	rep.Summary.Orphans = len(rep.Orphans)
	return rep, nil
}

func checkFile(p string, r fileRow, master []byte) string {
	f, err := os.Open(p)
	if err != nil {
		return FileMissing
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return FileMissing
	}
	h := sha256.New()
	if !r.enc {
		if st.Size() != r.size {
			return FileSizeMiss
		}
		if _, err := io.Copy(h, f); err != nil {
			return FileMissing
		}
		if r.sha != "" && hex.EncodeToString(h.Sum(nil)) != r.sha {
			return FileSHAMiss
		}
		return FilePlainLegacy
	}
	rd, err := filecrypt.NewReader(f, st.Size(), r.size, master, r.encID)
	if err != nil {
		switch {
		case errors.Is(err, filecrypt.ErrSizeMismatch):
			return FileSizeMiss
		case errors.Is(err, filecrypt.ErrIDMismatch):
			return FileIDMiss
		case errors.Is(err, filecrypt.ErrBadHeader):
			return FileBadHeader
		}
		var ce *filecrypt.ChunkError
		if errors.As(err, &ce) {
			return fmt.Sprintf("%s@chunk%d", FileTagFail, ce.Index)
		}
		return FileBadHeader
	}
	n, err := io.Copy(h, rd)
	if err != nil {
		var ce *filecrypt.ChunkError
		if errors.As(err, &ce) {
			return fmt.Sprintf("%s@chunk%d", FileTagFail, ce.Index)
		}
		return fmt.Sprintf("%s@chunk?", FileTagFail)
	}
	if n != r.size || (r.sha != "" && hex.EncodeToString(h.Sum(nil)) != r.sha) {
		return FileSHAMiss
	}
	return FileOK
}
