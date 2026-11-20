package dbcli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mhenderik/sendora/internal/platform"
	"github.com/mhenderik/sendora/internal/store"
)

// tenantImport converts the single-tenant installation in dataDir into the
// tenant slug of a multi-tenant installation (offline; stop the server first).
// It resumes an interrupted conversion, and -abort undoes an unfinished one.
func tenantImport(dataDir, keyFile, sourceKeyFile, slug, name string, abort bool) error {
	if dataDir == "" {
		return errors.New("data-dir is required (the DATA_DIR of the stopped single-tenant installation)")
	}
	master, err := store.ReadDatabaseKey(keyFile)
	if err != nil {
		return err
	}
	defer clear(master)
	var srcKey []byte
	if sourceKeyFile != "" {
		if srcKey, err = store.ReadDatabaseKey(sourceKeyFile); err != nil {
			return fmt.Errorf("source key: %w", err)
		}
		defer clear(srcKey)
	}
	p, err := platform.Open(dataDir, master, true)
	if err != nil {
		return fmt.Errorf("platform database: %w", err)
	}
	defer p.Close()
	res, err := p.ImportSingleTenant(platform.ImportOptions{Slug: slug, Name: name, SourceKey: srcKey, Abort: abort,
		Logf: func(f string, a ...any) { fmt.Fprintf(stderr, f+"\n", a...) }})
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(res)
}
