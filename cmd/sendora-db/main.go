// sendora-db performs explicit offline exports. It never replaces a live DB.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mhenderik/sendora/internal/store"
)

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sendora-db keygen|migrate|backup|snapshot-plaintext|verify|verify-files|audit-verify|mfa-reset [flags]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	source := flags.String("source", "", "database source path; stop the app before offline operations")
	destination := flags.String("destination", "", "new output path (must not exist)")
	keyFile := flags.String("key-file", os.Getenv("SENDORA_DB_KEY_FILE"), "external 32-byte key file")
	plaintext := flags.Bool("plaintext", false, "explicit legacy plaintext verification")
	user := flags.String("user", "", "username for mfa-reset")
	dataDir := flags.String("data-dir", "", "data directory holding shares/ (verify-files; default: directory of -source)")
	asJSON := flags.Bool("json", false, "verify-files and audit-verify: machine-readable output")
	names := flags.Bool("names", false, "verify-files: also print share and file names (sensitive)")
	all := flags.Bool("all", false, "verify-files: list every row, not only problems")
	progress := flags.Bool("progress", false, "verify-files: report progress on stderr (default: only when stderr is a terminal)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if args[0] == "keygen" {
		if *keyFile == "" {
			return errors.New("key-file is required; use a path outside the data volume and source tree")
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		f, err := os.OpenFile(*keyFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(key)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			os.Remove(*keyFile)
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Println("Key created. Keep a separate protected recovery copy; losing the key makes the database unrecoverable.")
		return nil
	}
	if *source == "" {
		return errors.New("source is required")
	}
	if args[0] == "snapshot-plaintext" {
		if *destination == "" {
			return errors.New("destination is required")
		}
		if err := store.SnapshotPlaintext(*source, *destination); err != nil {
			return err
		}
		fmt.Println("Verified standalone plaintext rollback snapshot created; protect it as sensitive data.")
		return nil
	}
	var key []byte
	if !(args[0] == "verify" && *plaintext) {
		var err error
		key, err = store.ReadDatabaseKey(*keyFile)
		if err != nil {
			return err
		}
	}
	switch args[0] {
	case "migrate":
		if *destination == "" {
			return errors.New("destination is required")
		}
		if err := store.ExportDatabase(*source, *destination, nil, key); err != nil {
			return err
		}
		fmt.Println("Encrypted export verified. Source unchanged; no replacement or restart performed.")
	case "backup":
		if *destination == "" {
			return errors.New("destination is required")
		}
		if err := store.ExportDatabase(*source, *destination, key, key); err != nil {
			return err
		}
		fmt.Println("Encrypted backup verified. Preserve the external key separately.")
	case "mfa-reset":
		if *user == "" {
			return errors.New("user is required")
		}
		if err := store.ResetUserMFA(*source, key, *user); err != nil {
			return err
		}
		fmt.Println("All multi-factor factors and sessions of the user were removed. Stop the application before running this offline step.")
	case "verify-files":
		dir := *dataDir
		if dir == "" {
			dir = filepath.Dir(*source)
		}
		return verifyFiles(*source, dir, key, verifyFlags{json: *asJSON, names: *names, all: *all, progress: *progress})
	case "verify":
		counts, err := store.VerifyDatabase(*source, key)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(counts)
	case "audit-verify":
		st, err := store.VerifyAuditDatabase(*source, key)
		if err != nil {
			return err
		}
		if *asJSON {
			if err := json.NewEncoder(os.Stdout).Encode(st); err != nil {
				return err
			}
		} else if st.OK {
			fmt.Printf("Audit chain verified: %d records, last seq %d, checkpoint seq %d.\n", st.Count, st.LastSeq, st.CheckpointSeq)
		} else {
			fmt.Printf("Audit chain BROKEN at seq %d (%s). %d records examined.\n", st.FirstBroken, st.Reason, st.Count)
		}
		if !st.OK {
			return fmt.Errorf("audit chain broken at seq %d", st.FirstBroken)
		}
	default:
		return errors.New("unknown command")
	}
	return nil
}

// Output of verify-files; replaced in tests.
var (
	stdout           io.Writer = os.Stdout
	stderr           io.Writer = os.Stderr
	stderrIsTerminal           = func() bool {
		st, err := os.Stderr.Stat()
		return err == nil && st.Mode()&os.ModeCharDevice != 0
	}
)

// progressEvery is how many files verify-files checks between progress lines.
var progressEvery = 1000

type verifyFlags struct{ json, names, all, progress bool }

// verifyFiles streams the verify-files report: by default only rows with a
// problem and orphans, with -all every row, then a summary. With -json the
// output is JSON Lines: one object per reported row ("type":"file") and
// orphan ("type":"orphan"), then one "type":"summary" object. Rows appear in
// row id order. Names appear only with -names; keys, plaintext and crypto
// error text never do. Any problem makes the command fail.
func verifyFiles(source, dataDir string, key []byte, f verifyFlags) error {
	out := bufio.NewWriter(stdout)
	enc := json.NewEncoder(out)
	var writeErr error
	emit := func(v any, line string) {
		if writeErr != nil {
			return
		}
		if f.json {
			writeErr = enc.Encode(v)
		} else {
			_, writeErr = fmt.Fprintln(out, line)
		}
	}
	opt := store.VerifyOptions{Names: f.names}
	opt.File = func(c store.FileCheck) {
		if !f.all && !c.Problem() {
			return
		}
		line := fmt.Sprintf("file id=%d path=%s %s", c.ID, c.PathHash, c.Status)
		if f.names {
			line += fmt.Sprintf(" share=%q file=%q", c.Share, c.Name)
		}
		emit(struct {
			Type string `json:"type"`
			store.FileCheck
		}{"file", c}, line)
		if c.Problem() {
			// Problems are visible at once, not only when the buffer fills.
			if err := out.Flush(); err != nil && writeErr == nil {
				writeErr = err
			}
		}
	}
	opt.Orphan = func(o store.OrphanFile) {
		line := fmt.Sprintf("orphan path=%s", o.PathHash)
		if f.names {
			line += fmt.Sprintf(" file=%q", o.Name)
		}
		emit(struct {
			Type string `json:"type"`
			store.OrphanFile
		}{"orphan", o}, line)
	}
	terminal := stderrIsTerminal()
	if f.progress || terminal {
		opt.Progress = func(done, total int) {
			if done%progressEvery != 0 && done != total {
				return
			}
			if terminal {
				fmt.Fprintf(stderr, "\rverify-files: %d/%d files checked", done, total)
				if done == total {
					fmt.Fprintln(stderr)
				}
			} else {
				fmt.Fprintf(stderr, "verify-files: %d/%d files checked\n", done, total)
			}
		}
	}
	s, err := store.VerifyFilesStream(source, dataDir, key, opt)
	if err != nil {
		out.Flush()
		return err
	}
	emit(struct {
		Type string `json:"type"`
		store.FilesSummary
	}{"summary", s}, fmt.Sprintf("summary rows=%d encrypted=%d plain=%d ok=%d plain-legacy=%d failed=%d orphans=%d",
		s.Rows, s.Encrypted, s.Plain, s.OK, s.Legacy, s.Failed, s.Orphans))
	if err := out.Flush(); err != nil && writeErr == nil {
		writeErr = err
	}
	if writeErr != nil {
		return writeErr
	}
	if n := s.Failed + s.Orphans; n != 0 {
		return fmt.Errorf("verify-files: %d problem(s) found", n)
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
