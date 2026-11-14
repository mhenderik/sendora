// sendora-db performs explicit offline exports. It never replaces a live DB.
package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/mhenderik/sendora/internal/store"
)

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sendora-db keygen|migrate|backup|snapshot-plaintext|verify|mfa-reset [flags]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	source := flags.String("source", "", "database source path; stop the app before offline operations")
	destination := flags.String("destination", "", "new output path (must not exist)")
	keyFile := flags.String("key-file", os.Getenv("SENDORA_DB_KEY_FILE"), "external 32-byte key file")
	plaintext := flags.Bool("plaintext", false, "explicit legacy plaintext verification")
	user := flags.String("user", "", "username for mfa-reset")
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
	case "verify":
		counts, err := store.VerifyDatabase(*source, key)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(counts)
	default:
		return errors.New("unknown command")
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
