package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/mhenderik/sendora/internal/filecrypt"
	"github.com/mhenderik/sendora/internal/storage"
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

type fileRow struct {
	id             int64
	name, fullPath string
	size           int64
	disk, sha      string
	enc            bool
	encID          string
	lid, share     string
	loc            int64
	objKey         string
}

// Problem reports whether the row failed a check (anything but ok and
// plain-legacy).
func (c FileCheck) Problem() bool { return c.Status != FileOK && c.Status != FilePlainLegacy }

// VerifyOptions controls VerifyFilesStream.
type VerifyOptions struct {
	// Names adds share and file names to the results (sensitive).
	Names bool
	// Workers is the number of files checked in parallel (default 4).
	Workers int
	// File receives every row's result in ascending row id order.
	File func(FileCheck)
	// Orphan receives every orphan, sorted by path hash, after all rows.
	Orphan func(OrphanFile)
	// Progress is called after each row with the rows done and the total.
	Progress func(done, total int)
	// OpenBackend replaces storage.Open (tests).
	OpenBackend func(context.Context, storage.Config) (storage.Backend, error)
}

// DefaultVerifyWorkers is the number of files VerifyFilesStream checks at once.
const DefaultVerifyWorkers = 4

// VerifyFiles checks every stored share file of the database at source against
// its row: header, stored size, file id, every chunk tag and the plaintext
// SHA-256. It also lists files on disk that no row refers to. It reads only.
func VerifyFiles(source, dataDir string, key []byte, names bool) (*FilesReport, error) {
	rep := &FilesReport{Files: []FileCheck{}, Orphans: []OrphanFile{}}
	sum, err := VerifyFilesStream(source, dataDir, key, VerifyOptions{
		Names:  names,
		File:   func(c FileCheck) { rep.Files = append(rep.Files, c) },
		Orphan: func(o OrphanFile) { rep.Orphans = append(rep.Orphans, o) },
	})
	if err != nil {
		return nil, err
	}
	rep.Summary = sum
	return rep, nil
}

// VerifyStoredFiles runs VerifyFilesStream on this store's own database.
func (s *Store) VerifyStoredFiles(opt VerifyOptions) (FilesSummary, error) {
	return VerifyFilesStream(s.Path, s.DataDir, s.cipherKey, opt)
}

// VerifyFilesStream is VerifyFiles for large stores: rows are checked by a
// small worker pool and handed to opt.File one by one in row id order instead
// of being collected, then orphans to opt.Orphan. It returns the summary.
func VerifyFilesStream(source, dataDir string, key []byte, opt VerifyOptions) (FilesSummary, error) {
	var sum FilesSummary
	master, err := filecrypt.MasterKey(key)
	if err != nil {
		return sum, err
	}
	db, err := openCipherDB(source, key, true)
	if err != nil {
		return sum, err
	}
	defer db.Close()
	// A database from before 0.2.0 that this binary has not opened yet has no
	// storage columns: every file is local then.
	var probe int64
	perr := db.QueryRow(`SELECT storage_id FROM files LIMIT 1`).Scan(&probe)
	hasLoc := perr == nil || errors.Is(perr, sql.ErrNoRows)
	q := `SELECT f.id,f.name,f.full_path,f.size,f.disk_path,f.sha256,f.enc,f.enc_id,s.long_id,s.name,0,'' FROM files f JOIN shares s ON s.id=f.share_id ORDER BY f.id`
	if hasLoc {
		q = `SELECT f.id,f.name,f.full_path,f.size,f.disk_path,f.sha256,f.enc,f.enc_id,s.long_id,s.name,f.storage_id,f.object_key FROM files f JOIN shares s ON s.id=f.share_id ORDER BY f.id`
	}
	rows, err := db.Query(q)
	if err != nil {
		return sum, errors.New("files table cannot be read")
	}
	var list []fileRow
	for rows.Next() {
		var r fileRow
		var enc int
		if err := rows.Scan(&r.id, &r.name, &r.fullPath, &r.size, &r.disk, &r.sha, &enc, &r.encID, &r.lid, &r.share, &r.loc, &r.objKey); err != nil {
			rows.Close()
			return sum, errors.New("files table cannot be read")
		}
		r.enc = enc == 1
		list = append(list, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return sum, errors.New("files table cannot be read")
	}

	// Resolve every object first: the orphan scan needs the complete set.
	backends := NewBackends(db, dataDir)
	if opt.OpenBackend != nil {
		backends.OpenFn = opt.OpenBackend
	}
	ctx := context.Background()
	keys := make([]string, len(list))
	known := map[int64]map[string]bool{LocalLocation: {}}
	for i, r := range list {
		if k, kerr := FileObjectKey(r.loc, r.lid, r.disk, r.objKey); kerr == nil {
			keys[i] = k
			if known[r.loc] == nil {
				known[r.loc] = map[string]bool{}
			}
			known[r.loc][k] = true
		}
	}
	check := func(i int) FileCheck {
		r := list[i]
		c := FileCheck{ID: r.id, Enc: r.enc}
		if opt.Names {
			c.Name, c.Share = r.fullPath, r.share
		}
		if keys[i] == "" {
			c.PathHash, c.Status = "00000000", FileMissing
			return c
		}
		c.PathHash = objectHash(r.loc, keys[i])
		be, err := backends.Get(ctx, r.loc)
		if err != nil {
			c.Status = FileMissing
			return c
		}
		obj, err := be.Open(ctx, keys[i])
		if err != nil {
			c.Status = FileMissing
			return c
		}
		c.Status = checkObject(obj, r, master)
		obj.Close()
		return c
	}

	workers := opt.Workers
	if workers <= 0 {
		workers = DefaultVerifyWorkers
	}
	// Workers may run at most window rows ahead of the next row to report,
	// which bounds the results held for reordering.
	window := workers * 16
	type result struct {
		idx int
		c   FileCheck
	}
	ahead := make(chan struct{}, window)
	jobs := make(chan int)
	results := make(chan result, window)
	go func() {
		for i := range list {
			ahead <- struct{}{}
			jobs <- i
		}
		close(jobs)
	}()
	for w := 0; w < workers; w++ {
		go func() {
			for i := range jobs {
				results <- result{i, check(i)}
			}
		}()
	}
	pending := map[int]FileCheck{}
	for next := 0; next < len(list); {
		res := <-results
		pending[res.idx] = res.c
		for {
			c, ok := pending[next]
			if !ok {
				break
			}
			delete(pending, next)
			<-ahead
			next++
			sum.Rows++
			if c.Enc {
				sum.Encrypted++
			} else {
				sum.Plain++
			}
			switch c.Status {
			case FileOK:
				sum.OK++
			case FilePlainLegacy:
				sum.Legacy++
			default:
				sum.Failed++
			}
			if opt.File != nil {
				opt.File(c)
			}
			if opt.Progress != nil {
				opt.Progress(next, len(list))
			}
		}
	}

	// Only files directly inside a share directory are stored files;
	// directories inside a share directory are skipped.
	var orphans []OrphanFile
	shares := filepath.Join(dataDir, "shares")
	entries, _ := os.ReadDir(shares)
	for _, d := range entries {
		if !d.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(shares, d.Name()))
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			key := "shares/" + d.Name() + "/" + f.Name()
			if known[LocalLocation][key] {
				continue
			}
			o := OrphanFile{PathHash: objectHash(LocalLocation, key)}
			if opt.Names {
				o.Name = f.Name()
			}
			orphans = append(orphans, o)
		}
	}
	// Objects under files/ of every remote location that no row refers to.
	if hasLoc {
		locRows, err := db.Query(`SELECT id FROM storage_locations ORDER BY id`)
		if err == nil {
			var locs []int64
			for locRows.Next() {
				var id int64
				if locRows.Scan(&id) == nil {
					locs = append(locs, id)
				}
			}
			locRows.Close()
			for _, id := range locs {
				be, err := backends.Get(ctx, id)
				if err != nil {
					return sum, fmt.Errorf("storage location %d cannot be opened: %v", id, err)
				}
				err = be.List(ctx, storage.FilePrefix, func(i storage.Info) error {
					if !known[id][i.Key] {
						o := OrphanFile{PathHash: objectHash(id, i.Key)}
						if opt.Names {
							o.Name = fmt.Sprintf("location %d: %s", id, i.Key)
						}
						orphans = append(orphans, o)
					}
					return nil
				})
				if err != nil {
					return sum, fmt.Errorf("storage location %d cannot be listed: %v", id, err)
				}
			}
		}
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].PathHash < orphans[j].PathHash })
	sum.Orphans = len(orphans)
	if opt.Orphan != nil {
		for _, o := range orphans {
			opt.Orphan(o)
		}
	}
	return sum, nil
}

// objectHash identifies a stored object without naming it: the local key is
// the path below the data directory, as in earlier releases.
func objectHash(loc int64, key string) string {
	if loc != LocalLocation {
		key = fmt.Sprintf("location-%d/%s", loc, key)
	}
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:4])
}

func checkObject(f storage.Object, r fileRow, master []byte) string {
	st := struct{ size int64 }{f.Size()}
	h := sha256.New()
	if !r.enc {
		if st.size != r.size {
			return FileSizeMiss
		}
		if _, err := io.Copy(h, io.NewSectionReader(f, 0, st.size)); err != nil {
			return FileMissing
		}
		if r.sha != "" && hex.EncodeToString(h.Sum(nil)) != r.sha {
			return FileSHAMiss
		}
		return FilePlainLegacy
	}
	rd, err := filecrypt.NewReader(nopCloser{f}, st.size, r.size, master, r.encID)
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
