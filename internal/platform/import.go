package platform

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mhenderik/sendora/internal/store"
)

// Offline conversion of a single-tenant data directory into a tenant
// ("sendora-db tenant-import"). The server must be stopped.
//
// Choice for the share files: they were encrypted with keys derived from the
// single-tenant database key. Re-encrypting every file would rewrite all data
// (slow, and a crash in the middle leaves a mix); instead the imported tenant
// keeps that key as its "legacy root" (key_mode=legacy): the database is
// re-encrypted with the tenant's derived key, while the file master key and
// the audit chain key are still derived from the old key, so every file stays
// decryptable and the audit chain stays verifiable without being rewritten.
// When the old key is the platform's master key nothing extra is stored; a
// different old key is stored in the (encrypted) platform database.
//
// Safety: the original database is never modified. The conversion copies it
// with sqlcipher_export under the new key (verified), moves the share,
// upload, branding and background directories (a rename on the same file
// system), writes re-encrypted copies of the backups, verifies the result
// (database integrity, audit chain, every share file decrypts) and only then
// moves the original database and backups into DATA_DIR/pre-tenant-import-*/.
// A journal (DATA_DIR/tenant-import.json) makes the command resumable after a
// crash and lets -abort put everything back until the final step.

// ImportJournal is the progress file of a conversion.
const ImportJournal = "tenant-import.json"

// Import phases, in order.
const (
	phaseRegistered = "registered"
	phaseDatabase   = "database"
	phaseMoved      = "moved"
	phaseVerified   = "verified"
)

var movedDirs = []string{"shares", "uploads", "branding", "backgrounds"}

type importJournal struct {
	TenantID string `json:"tenant_id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Phase    string `json:"phase"`
	Started  string `json:"started"`
}

// ImportOptions configures ImportSingleTenant.
type ImportOptions struct {
	Slug, Name string
	// SourceKey is the key of the single-tenant database (nil = the master key).
	SourceKey []byte
	// Abort reverts an unfinished conversion instead of continuing it.
	Abort bool
	Logf  func(string, ...any)
}

// ImportResult describes a finished conversion.
type ImportResult struct {
	TenantID string `json:"tenant_id"`
	Slug     string `json:"slug"`
	Files    int    `json:"files_verified"`
	Backups  int    `json:"backups"`
	Archive  string `json:"archive"`
	Aborted  bool   `json:"aborted,omitempty"`
}

func (p *Platform) journalPath() string { return filepath.Join(p.DataDir, ImportJournal) }

func (p *Platform) readJournal() (*importJournal, error) {
	b, err := os.ReadFile(p.journalPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j := &importJournal{}
	if err := json.Unmarshal(b, j); err != nil || !store.TenantIDPattern.MatchString(j.TenantID) {
		return nil, errors.New("the conversion journal is unreadable; inspect " + ImportJournal + " by hand")
	}
	return j, nil
}

// importFailAfter makes the conversion stop right after the named phase
// (tests of resume and abort).
var importFailAfter string

var errImportInjected = errors.New("conversion stopped for a test")

func (p *Platform) writeJournal(j *importJournal) error {
	b, _ := json.MarshalIndent(j, "", "  ")
	tmp := p.journalPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, p.journalPath()); err != nil {
		return err
	}
	if importFailAfter != "" && importFailAfter == j.Phase {
		return errImportInjected
	}
	return nil
}

// ImportSingleTenant converts the single-tenant installation in the platform's
// data directory into a tenant, or continues/aborts an unfinished conversion.
func (p *Platform) ImportSingleTenant(opt ImportOptions) (*ImportResult, error) {
	logf := opt.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	srcKey := opt.SourceKey
	if srcKey == nil {
		srcKey = p.master
	}
	if len(srcKey) != 32 {
		return nil, errors.New("source key must contain 32 bytes")
	}
	j, err := p.readJournal()
	if err != nil {
		return nil, err
	}
	if opt.Abort {
		if j == nil {
			return nil, errors.New("no unfinished conversion to abort")
		}
		return p.abortImport(j, logf)
	}
	srcDB, err := store.DBPath(p.DataDir)
	if err != nil {
		return nil, err
	}
	if j == nil {
		if _, err := os.Stat(srcDB); err != nil {
			return nil, errors.New("no single-tenant database (" + store.DBFile + ") in the data directory: nothing to convert")
		}
		if _, err := os.Stat(filepath.Join(p.DataDir, store.ImportMarker)); err == nil {
			return nil, errors.New("an interrupted database import must be completed first: start the server once in single-tenant mode")
		}
		if !store.DatabaseReadable(srcDB, srcKey) {
			return nil, errors.New("the single-tenant database cannot be read with the source key")
		}
		if !ValidSlug(opt.Slug) {
			return nil, errors.New("invalid or reserved subdomain")
		}
		if _, err := p.TenantBySlug(opt.Slug); err == nil {
			return nil, ErrSlugTaken
		}
		j = &importJournal{TenantID: NewTenantID(), Slug: opt.Slug, Name: opt.Name, Phase: phaseRegistered, Started: now()}
		if j.Name == "" {
			j.Name = opt.Slug
		}
		if err := p.writeJournal(j); err != nil {
			return nil, err
		}
		logf("tenant-import: converting into tenant %s (%s)", j.Slug, j.TenantID)
	} else {
		logf("tenant-import: resuming the conversion into tenant %s (%s) after phase %q", j.Slug, j.TenantID, j.Phase)
	}
	if _, err := p.Tenant(j.TenantID); errors.Is(err, ErrNotFound) {
		var root []byte
		if subtle.ConstantTimeCompare(srcKey, p.master) != 1 {
			root = append([]byte(nil), srcKey...)
		}
		t := &Tenant{ID: j.TenantID, Slug: j.Slug, Name: j.Name, Status: StatusImporting, CreatedAt: now(), KeyMode: KeyLegacy}
		if err := p.insertTenant(t, root); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	t, err := p.Tenant(j.TenantID)
	if err != nil {
		return nil, err
	}
	dir, err := p.TenantDir(t.ID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dbKey, root, err := p.TenantKeys(t)
	if err != nil {
		return nil, err
	}
	defer clear(dbKey)
	defer clear(root)
	dstDB := filepath.Join(dir, store.DBFile)

	if j.Phase == phaseRegistered {
		for _, s := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(dstDB + s); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
		logf("tenant-import: re-encrypting the database with the tenant key")
		if err := store.ExportDatabase(srcDB, dstDB, srcKey, dbKey); err != nil {
			return nil, fmt.Errorf("database copy: %w", err)
		}
		j.Phase = phaseDatabase
		if err := p.writeJournal(j); err != nil {
			return nil, err
		}
	}
	res := &ImportResult{TenantID: t.ID, Slug: t.Slug}
	if j.Phase == phaseDatabase {
		for _, d := range movedDirs {
			if err := moveDir(filepath.Join(p.DataDir, d), filepath.Join(dir, d)); err != nil {
				return nil, fmt.Errorf("moving %s: %w", d, err)
			}
		}
		n, err := copyBackups(filepath.Join(p.DataDir, "backups"), filepath.Join(dir, "backups"), srcKey, dbKey)
		if err != nil {
			return nil, fmt.Errorf("backups: %w", err)
		}
		logf("tenant-import: moved the share directories; %d backup(s) re-encrypted or copied", n)
		j.Phase = phaseMoved
		if err := p.writeJournal(j); err != nil {
			return nil, err
		}
	}
	if j.Phase == phaseMoved {
		logf("tenant-import: verifying the database, the audit chain and every share file")
		src, err := store.VerifyAuditDatabaseRoot(srcDB, srcKey, srcKey)
		if err != nil {
			return nil, fmt.Errorf("audit chain of the original: %w", err)
		}
		dst, err := store.VerifyAuditDatabaseRoot(dstDB, dbKey, rootOr(root, dbKey))
		if err != nil {
			return nil, fmt.Errorf("audit chain of the tenant: %w", err)
		}
		if src.OK != dst.OK || src.LastSeq != dst.LastSeq || src.FirstBroken != dst.FirstBroken {
			return nil, errors.New("the tenant's audit chain does not verify like the original's")
		}
		sum, err := store.VerifyFilesStream(dstDB, dir, dbKey, store.VerifyOptions{Root: root})
		if err != nil {
			return nil, fmt.Errorf("file verification: %w", err)
		}
		if sum.Failed > 0 {
			return nil, fmt.Errorf("%d share file(s) do not verify; nothing was finalised (run sendora-db tenant-import -abort to undo, or investigate with verify-files)", sum.Failed)
		}
		res.Files = sum.Rows
		st, err := store.OpenTenant(dir, dbKey, root, false)
		if err != nil {
			return nil, fmt.Errorf("opening the tenant: %w", err)
		}
		_, aerr := st.AuditAppend(store.AuditRecord{Category: "system", ActorName: "system", ActorRole: "system", Action: "tenant_imported", Outcome: "ok",
			Detail: "converted from a single-tenant installation into tenant " + t.Slug})
		st.Close()
		if aerr != nil {
			return nil, aerr
		}
		j.Phase = phaseVerified
		if err := p.writeJournal(j); err != nil {
			return nil, err
		}
	}
	// Finalise: archive the originals, activate the tenant.
	archive := filepath.Join(p.DataDir, "pre-tenant-import-"+time.Now().UTC().Format("20060102-150405"))
	if err := os.MkdirAll(archive, 0o700); err != nil {
		return nil, err
	}
	for _, s := range []string{"-wal", "-shm", store.PreImportSuffix, ""} {
		if err := os.Rename(srcDB+s, filepath.Join(archive, store.DBFile+s)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	if err := os.Rename(filepath.Join(p.DataDir, "backups"), filepath.Join(archive, "backups")); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if ents, err := os.ReadDir(filepath.Join(dir, "backups")); err == nil {
		res.Backups = len(ents)
	}
	if _, err := p.St.DB.Exec(`UPDATE tenants SET status='active' WHERE id=? AND status='importing'`, t.ID); err != nil {
		return nil, err
	}
	p.reloadDomains()
	p.Audit(0, "system", "tenant_imported", "tenant "+t.Slug+" ("+t.ID+")", "ok", "converted from a single-tenant data directory; originals archived in "+filepath.Base(archive), "")
	if err := os.Remove(p.journalPath()); err != nil {
		return nil, err
	}
	res.Archive = archive
	logf("tenant-import: done. The original database and backups are in %s; remove that directory once you have checked the tenant.", archive)
	return res, nil
}

func rootOr(root, key []byte) []byte {
	if root != nil {
		return root
	}
	return key
}

// moveDir renames src to dst. A missing src is fine (already moved, or never
// created); an existing empty dst is replaced; a non-empty dst is an error.
func moveDir(src, dst string) error {
	if _, err := os.Lstat(src); os.IsNotExist(err) {
		return os.MkdirAll(dst, 0o700)
	} else if err != nil {
		return err
	}
	if ents, err := os.ReadDir(dst); err == nil {
		if len(ents) > 0 {
			return fmt.Errorf("%s exists and is not empty", dst)
		}
		if err := os.Remove(dst); err != nil {
			return err
		}
	}
	return os.Rename(src, dst)
}

// copyBackups writes every backup of src into dst: encrypted ones re-encrypted
// with the tenant key, others (pre-migration plaintext snapshots) copied
// unchanged. Already present destination files are skipped (resume).
func copyBackups(src, dst string, srcKey, dstKey []byte) (int, error) {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return 0, err
	}
	ents, err := os.ReadDir(src)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if _, err := os.Stat(to); err == nil {
			n++
			continue
		}
		tmp := to + ".partial"
		os.Remove(tmp)
		if isEncryptedDB(from) && store.DatabaseReadable(from, srcKey) {
			if err := store.ExportDatabase(from, tmp, srcKey, dstKey); err != nil {
				return n, fmt.Errorf("%s: %w", e.Name(), err)
			}
		} else if err := copyFile(from, tmp); err != nil {
			return n, err
		}
		if err := os.Rename(tmp, to); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func isEncryptedDB(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	h := make([]byte, 16)
	if _, err := io.ReadFull(f, h); err != nil {
		return false
	}
	return !bytes.Equal(h, []byte("SQLite format 3\x00"))
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// abortImport puts an unfinished conversion back: directories return to the
// data directory, the tenant's copies are removed, the tenant row goes.
func (p *Platform) abortImport(j *importJournal, logf func(string, ...any)) (*ImportResult, error) {
	dir, err := p.TenantDir(j.TenantID)
	if err != nil {
		return nil, err
	}
	for _, d := range movedDirs {
		from, to := filepath.Join(dir, d), filepath.Join(p.DataDir, d)
		if _, err := os.Lstat(from); os.IsNotExist(err) {
			continue
		}
		if _, err := os.Lstat(to); os.IsNotExist(err) {
			if err := os.Rename(from, to); err != nil {
				return nil, err
			}
			continue
		}
		if ents, err := os.ReadDir(from); err != nil || len(ents) > 0 {
			return nil, fmt.Errorf("both %s and the tenant's %s exist with content; resolve by hand before aborting", to, d)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if _, err := p.St.DB.Exec(`DELETE FROM tenants WHERE id=? AND status='importing'`, j.TenantID); err != nil {
		return nil, err
	}
	p.reloadDomains()
	p.Audit(0, "system", "tenant_import_aborted", "tenant "+j.Slug+" ("+j.TenantID+")", "ok", "", "")
	if err := os.Remove(p.journalPath()); err != nil {
		return nil, err
	}
	logf("tenant-import: aborted; the single-tenant data directory is as before")
	return &ImportResult{TenantID: j.TenantID, Slug: j.Slug, Aborted: true}, nil
}
