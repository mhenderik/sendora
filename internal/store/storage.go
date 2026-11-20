package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mhenderik/sendora/internal/storage"
)

// Storage locations. Every files row names where its (encrypted) bytes live:
//
//	storage_id = 0   the local data directory in the layout of every earlier
//	                 release: DATA_DIR/shares/<long id>/<base name of disk_path>
//	storage_id = n   storage_locations row n (an S3 bucket, Azure container or
//	                 GCS bucket with a prefix) under object_key "files/<random>"
//
// A location keeps the configuration (including credentials; the database is
// SQLCipher-encrypted) it was last used with, so switching the backend or the
// bucket never makes earlier files unreadable: they stay where they are until
// they are migrated.

// LocalLocation is the storage_id of the local data directory.
const LocalLocation int64 = 0

func (s *Store) migrateStorage() error {
	if _, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS storage_locations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  backend TEXT NOT NULL,
  identity TEXT NOT NULL UNIQUE,
  config TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS storage_pending_deletes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  location_id INTEGER NOT NULL,
  object_key TEXT NOT NULL,
  not_before DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  attempts INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS storage_migrations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  status TEXT NOT NULL DEFAULT 'running',
  target_location INTEGER NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  total INTEGER NOT NULL DEFAULT 0,
  done INTEGER NOT NULL DEFAULT 0,
  failed INTEGER NOT NULL DEFAULT 0,
  bytes INTEGER NOT NULL DEFAULT 0,
  started_by TEXT NOT NULL DEFAULT '',
  started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at DATETIME,
  error TEXT NOT NULL DEFAULT ''
);`); err != nil {
		return err
	}
	for _, c := range []struct{ table, name, decl string }{
		{"files", "storage_id", "INTEGER NOT NULL DEFAULT 0"},
		{"files", "object_key", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := addColumnIfMissing(s.DB, c.table, c.name, c.decl); err != nil {
			return err
		}
	}
	// Deleting a files row (one file, a share, a user, an erasure: also through
	// ON DELETE CASCADE) queues its remote object for deletion in the same
	// transaction, so no code path can forget a remote object. Local files keep
	// their directory-based removal.
	_, err := s.DB.Exec(`CREATE TRIGGER IF NOT EXISTS files_remote_deleted AFTER DELETE ON files
WHEN OLD.storage_id<>0 AND OLD.object_key<>''
BEGIN INSERT INTO storage_pending_deletes(location_id,object_key) VALUES(OLD.storage_id,OLD.object_key); END;`)
	return err
}

func addColumnIfMissing(db *sql.DB, table, name, decl string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var n, typ string
		var def any
		if err := rows.Scan(&cid, &n, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if n == name {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || found {
		return err
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, name, decl))
	return err
}

// EnsureLocation returns the id of the location with cfg's identity, creating
// it or refreshing its stored configuration (rotated credentials). The local
// backend is always LocalLocation.
func EnsureLocation(db *sql.DB, cfg storage.Config) (int64, error) {
	if cfg.Backend == storage.KindLocal || cfg.Backend == "" {
		return LocalLocation, nil
	}
	js, err := cfg.Marshal()
	if err != nil {
		return 0, err
	}
	var id int64
	err = db.QueryRow(`INSERT INTO storage_locations(backend,identity,config) VALUES(?,?,?)
		ON CONFLICT(identity) DO UPDATE SET config=excluded.config, updated_at=CASE WHEN config<>excluded.config THEN CURRENT_TIMESTAMP ELSE updated_at END
		RETURNING id`, cfg.Backend, cfg.Identity(), js).Scan(&id)
	return id, err
}

// LoadLocation returns the configuration of location id.
func LoadLocation(db *sql.DB, dataDir string, id int64) (storage.Config, error) {
	if id == LocalLocation {
		return storage.Config{Backend: storage.KindLocal, LocalRoot: dataDir}, nil
	}
	var js string
	if err := db.QueryRow(`SELECT config FROM storage_locations WHERE id=?`, id).Scan(&js); err != nil {
		return storage.Config{}, fmt.Errorf("storage location %d unavailable", id)
	}
	return storage.Unmarshal(js)
}

// FileObjectKey is the object key of a files row in its location. For local
// files it is derived like SharePath (validated long ID, base name only); a
// remote key must have the generated form. Anything else is refused.
func FileObjectKey(location int64, longID, diskPath, objectKey string) (string, error) {
	if location == LocalLocation {
		if !ValidLongID(longID) {
			return "", errUnsafeSharePath
		}
		base := filepath.Base(diskPath)
		if diskPath == "" || base == "." || base == ".." || base == string(filepath.Separator) || base != filepath.Clean(base) || strings.ContainsAny(base, "/\\") {
			return "", errUnsafeSharePath
		}
		return "shares/" + longID + "/" + base, nil
	}
	if !storage.ValidFileKey(objectKey) {
		return "", errUnsafeSharePath
	}
	return objectKey, nil
}

// Backends opens and caches the backend of each location. A location's
// backend is reopened when its stored configuration changed.
type Backends struct {
	db      *sql.DB
	dataDir string
	mu      sync.Mutex
	open    map[int64]cachedBackend
	// OpenFn opens a backend (tests replace it).
	OpenFn func(context.Context, storage.Config) (storage.Backend, error)
}

type cachedBackend struct {
	config string
	b      storage.Backend
}

func NewBackends(db *sql.DB, dataDir string) *Backends {
	return &Backends{db: db, dataDir: dataDir, open: map[int64]cachedBackend{}, OpenFn: storage.Open}
}

// Backends of this store.
func (s *Store) Backends() *Backends {
	s.backendsOnce.Do(func() { s.backends = NewBackends(s.DB, s.DataDir) })
	return s.backends
}

// Get returns the backend of location id.
func (b *Backends) Get(ctx context.Context, id int64) (storage.Backend, error) {
	cfg, err := LoadLocation(b.db, b.dataDir, id)
	if err != nil {
		return nil, err
	}
	js, _ := cfg.Marshal()
	b.mu.Lock()
	c, ok := b.open[id]
	b.mu.Unlock()
	if ok && c.config == js {
		return c.b, nil
	}
	be, err := b.OpenFn(ctx, cfg)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.open[id] = cachedBackend{js, be}
	b.mu.Unlock()
	return be, nil
}

// OpenFile opens the stored bytes of a files row.
func (b *Backends) OpenFile(ctx context.Context, location int64, longID, diskPath, objectKey string) (storage.Object, error) {
	key, err := FileObjectKey(location, longID, diskPath, objectKey)
	if err != nil {
		return nil, err
	}
	be, err := b.Get(ctx, location)
	if err != nil {
		return nil, err
	}
	return be.Open(ctx, key)
}

// ---------- queued deletion of remote objects ----------

// QueueDelete records a remote object for deletion not before the given time.
func QueueDelete(db interface {
	Exec(string, ...any) (sql.Result, error)
}, location int64, key string, notBefore time.Time) error {
	if location == LocalLocation {
		return nil
	}
	_, err := db.Exec(`INSERT INTO storage_pending_deletes(location_id,object_key,not_before) VALUES(?,?,?)`, location, key, notBefore.UTC())
	return err
}

// PendingDeletes is the number of remote objects still to be deleted.
func PendingDeletes(db *sql.DB) int64 {
	var n int64
	db.QueryRow(`SELECT COUNT(*) FROM storage_pending_deletes`).Scan(&n)
	return n
}

// ProcessPendingDeletes deletes due queued objects (at most limit) and returns
// how many were deleted and how many failed. A failure is retried later with a
// growing delay; deleting a missing object counts as success.
func ProcessPendingDeletes(ctx context.Context, db *sql.DB, backends *Backends, limit int) (done, failed int) {
	type job struct {
		id, loc  int64
		key      string
		attempts int
	}
	rows, err := db.Query(`SELECT id,location_id,object_key,attempts FROM storage_pending_deletes WHERE not_before<=? ORDER BY id LIMIT ?`, time.Now().UTC(), limit)
	if err != nil {
		return 0, 0
	}
	var jobs []job
	for rows.Next() {
		var j job
		if rows.Scan(&j.id, &j.loc, &j.key, &j.attempts) == nil {
			jobs = append(jobs, j)
		}
	}
	rows.Close()
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		err := errors.New("invalid object key")
		if storage.ValidFileKey(j.key) {
			var be storage.Backend
			if be, err = backends.Get(ctx, j.loc); err == nil {
				err = be.Delete(ctx, j.key)
			}
		}
		if err == nil {
			db.Exec(`DELETE FROM storage_pending_deletes WHERE id=?`, j.id)
			done++
			continue
		}
		failed++
		delay := time.Duration(min(j.attempts+1, 36)) * 10 * time.Minute
		db.Exec(`UPDATE storage_pending_deletes SET attempts=attempts+1, not_before=? WHERE id=?`, time.Now().UTC().Add(delay), j.id)
	}
	return done, failed
}

// LocationUsage is how many files (and plaintext bytes) live in one location.
type LocationUsage struct {
	ID          int64  `json:"id"`
	Backend     string `json:"backend"`
	Description string `json:"description"`
	Files       int64  `json:"files"`
	Bytes       int64  `json:"bytes"`
	Current     bool   `json:"current"`
}

// StorageUsageByLocation lists every location that holds files, plus current.
func StorageUsageByLocation(db *sql.DB, current int64) ([]LocationUsage, error) {
	rows, err := db.Query(`SELECT storage_id,COUNT(*),COALESCE(SUM(size),0) FROM files GROUP BY storage_id ORDER BY storage_id`)
	if err != nil {
		return nil, err
	}
	var out []LocationUsage
	seen := map[int64]bool{}
	for rows.Next() {
		var u LocationUsage
		if err := rows.Scan(&u.ID, &u.Files, &u.Bytes); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, u)
		seen[u.ID] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if !seen[current] {
		out = append(out, LocationUsage{ID: current})
	}
	for i := range out {
		out[i].Current = out[i].ID == current
		cfg, err := LoadLocation(db, "", out[i].ID)
		if err != nil {
			out[i].Backend, out[i].Description = "unknown", "unknown location"
			continue
		}
		out[i].Backend, out[i].Description = cfg.Backend, cfg.Describe()
	}
	return out, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
