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
	"time"

	"github.com/mhenderik/sendora/internal/filecrypt"
	"github.com/mhenderik/sendora/internal/storage"
)

// MigrateOptions controls MigrateFiles.
type MigrateOptions struct {
	// DeleteDelay > 0 queues each source object for deletion after this delay
	// (downloads that opened the old copy can finish); 0 deletes it at once.
	// Local source files are always removed at once.
	DeleteDelay time.Duration
	// Progress is called after every file.
	Progress func(MigrateStats)
	// Limit stops after this many files (0 = all; tests).
	Limit int
}

// MigrateStats counts a migration run.
type MigrateStats struct {
	Total   int   `json:"total"`
	Done    int   `json:"done"`
	Failed  int   `json:"failed"`
	Skipped int   `json:"skipped"` // removed or changed while being copied
	Bytes   int64 `json:"bytes"`
}

// ErrMigrateNoKey refuses to copy a plain legacy file to an object store
// without the key that would encrypt it first.
var ErrMigrateNoKey = errors.New("file encryption key unavailable: plain files are never copied to an object store")

type migrateRow struct {
	id, size, loc         int64
	sha, encID, key, disk string
	name, lid             string
	enc                   bool
}

// MigrateFiles copies every share file that does not live in target to
// target, verifies the copy (size, every GCM chunk tag, file id and the
// plaintext SHA-256), switches the files row and then deletes the source.
// Legacy plain files are encrypted on the way. A file that fails stays where
// it is (its copy is removed) and is counted; the run continues. It returns
// when every file was tried or ctx ends.
func MigrateFiles(ctx context.Context, db *sql.DB, dataDir string, master []byte, backends *Backends, target int64, opt MigrateOptions) (MigrateStats, error) {
	var st MigrateStats
	dst, err := backends.Get(ctx, target)
	if err != nil {
		return st, err
	}
	ids, err := migrateCandidates(db, target)
	if err != nil {
		return st, err
	}
	if opt.Limit > 0 && len(ids) > opt.Limit {
		ids = ids[:opt.Limit]
	}
	st.Total = len(ids)
	if opt.Progress != nil {
		opt.Progress(st)
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		var r migrateRow
		err := db.QueryRow(`SELECT f.id,f.size,f.storage_id,f.sha256,f.enc_id,f.object_key,f.disk_path,f.name,f.enc,s.long_id FROM files f JOIN shares s ON s.id=f.share_id WHERE f.id=?`, id).
			Scan(&r.id, &r.size, &r.loc, &r.sha, &r.encID, &r.key, &r.disk, &r.name, &r.enc, &r.lid)
		if err != nil || r.loc == target {
			st.Skipped++
		} else {
			n, skipped, err := migrateOne(ctx, db, dataDir, master, backends, dst, target, r, opt)
			switch {
			case err != nil:
				if ctx.Err() != nil {
					return st, ctx.Err()
				}
				st.Failed++
			case skipped:
				st.Skipped++
			default:
				st.Done++
				st.Bytes += n
			}
		}
		if opt.Progress != nil {
			opt.Progress(st)
		}
	}
	return st, nil
}

func migrateCandidates(db *sql.DB, target int64) ([]int64, error) {
	rows, err := db.Query(`SELECT id FROM files WHERE storage_id<>? ORDER BY id`, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MigrateRemaining is the number of files outside target.
func MigrateRemaining(db *sql.DB, target int64) int64 {
	var n int64
	db.QueryRow(`SELECT COUNT(*) FROM files WHERE storage_id<>?`, target).Scan(&n)
	return n
}

func migrateOne(ctx context.Context, db *sql.DB, dataDir string, master []byte, backends *Backends, dst storage.Backend, target int64, r migrateRow, opt MigrateOptions) (int64, bool, error) {
	srcKey, err := FileObjectKey(r.loc, r.lid, r.disk, r.key)
	if err != nil {
		return 0, false, err
	}
	src, err := backends.Get(ctx, r.loc)
	if err != nil {
		return 0, false, err
	}
	in, err := src.Open(ctx, srcKey)
	if err != nil {
		return 0, false, err
	}
	defer in.Close()

	// The new key: random in an object store; in the local data directory the
	// layout of every earlier release (the share directory must still exist).
	var newKey, newDisk string
	if target == LocalLocation {
		dir, derr := ShareDir(dataDir, r.lid)
		if derr != nil {
			return 0, false, derr
		}
		if _, serr := os.Stat(dir); serr != nil {
			return 0, true, nil // share removed meanwhile
		}
		base := randomHex(8) + "_" + filepath.Base(r.name)
		newKey, newDisk = "shares/"+r.lid+"/"+base, filepath.Join(dir, base)
		if _, err := FileObjectKey(LocalLocation, r.lid, newDisk, ""); err != nil {
			base = randomHex(8) + "_file"
			newKey, newDisk = "shares/"+r.lid+"/"+base, filepath.Join(dir, base)
		}
	} else if newKey, err = storage.NewFileKey(); err != nil {
		return 0, false, err
	}

	enc, encID, sha := r.enc, r.encID, r.sha
	if !r.enc {
		// A plain legacy file (uploaded before 0.1.2) is encrypted on the way;
		// without a key it may only move between local directories.
		if len(master) != 32 {
			if target != LocalLocation {
				return 0, false, ErrMigrateNoKey
			}
		} else {
			pr, pw := io.Pipe()
			h := sha256.New()
			var id string
			go func() {
				n, fid, err := filecrypt.Encrypt(pw, io.NewSectionReader(in, 0, in.Size()), master, h)
				if err == nil && n != r.size {
					err = errors.New("plain file size differs from its record")
				}
				id = fid
				pw.CloseWithError(err)
			}()
			err := dst.Put(ctx, newKey, pr, filecrypt.CipherSize(r.size))
			pr.CloseWithError(errors.New("upload ended"))
			if err != nil {
				dst.Delete(context.WithoutCancel(ctx), newKey)
				return 0, false, err
			}
			got := hex.EncodeToString(h.Sum(nil))
			if r.sha != "" && got != r.sha {
				dst.Delete(context.WithoutCancel(ctx), newKey)
				return 0, false, errors.New("plain source does not match its checksum")
			}
			enc, encID, sha = true, id, got
		}
	}
	if enc == r.enc {
		if err := dst.Put(ctx, newKey, io.NewSectionReader(in, 0, in.Size()), in.Size()); err != nil {
			dst.Delete(context.WithoutCancel(ctx), newKey)
			return 0, false, err
		}
	}
	if err := verifyCopy(ctx, dst, newKey, r.size, enc, encID, sha, master); err != nil {
		dst.Delete(context.WithoutCancel(ctx), newKey)
		return 0, false, err
	}

	objectKey, diskPath := newKey, ""
	if target == LocalLocation {
		objectKey, diskPath = "", newDisk
	}
	res, err := db.Exec(`UPDATE files SET storage_id=?,object_key=?,disk_path=?,enc=?,enc_id=?,sha256=? WHERE id=? AND storage_id=? AND object_key=? AND disk_path=? AND enc_id=?`,
		target, objectKey, diskPath, enc, encID, sha, r.id, r.loc, r.key, r.disk, r.encID)
	if err != nil {
		dst.Delete(context.WithoutCancel(ctx), newKey)
		return 0, false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		dst.Delete(context.WithoutCancel(ctx), newKey)
		return 0, true, nil // the file was removed or moved meanwhile
	}
	in.Close()
	if r.loc == LocalLocation || opt.DeleteDelay <= 0 {
		if err := src.Delete(ctx, srcKey); err != nil && r.loc != LocalLocation {
			QueueDelete(db, r.loc, srcKey, time.Now().Add(10*time.Minute))
		}
	} else {
		QueueDelete(db, r.loc, srcKey, time.Now().Add(opt.DeleteDelay))
	}
	return r.size, false, nil
}

// verifyCopy reads the stored copy back completely: the encrypted format's
// header, file id and every chunk tag, the plaintext length and SHA-256.
func verifyCopy(ctx context.Context, b storage.Backend, key string, size int64, enc bool, encID, sha string, master []byte) error {
	obj, err := b.Open(ctx, key)
	if err != nil {
		return err
	}
	defer obj.Close()
	var rd io.Reader = io.NewSectionReader(obj, 0, obj.Size())
	if enc {
		if len(master) != 32 {
			return ErrMigrateNoKey
		}
		fr, err := filecrypt.NewReader(nopCloser{obj}, obj.Size(), size, master, encID)
		if err != nil {
			return fmt.Errorf("copy fails authentication: %w", err)
		}
		rd = fr
	} else if obj.Size() != size {
		return errors.New("copy has the wrong size")
	}
	h := sha256.New()
	n, err := io.Copy(h, rd)
	if err != nil {
		return fmt.Errorf("copy fails authentication: %w", err)
	}
	if n != size || (sha != "" && hex.EncodeToString(h.Sum(nil)) != sha) {
		return errors.New("copy does not match the recorded checksum")
	}
	return nil
}

type nopCloser struct{ io.ReaderAt }

func (nopCloser) Close() error { return nil }
