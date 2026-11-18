//go:build !linux

package app

// DisableCoreDumps is a no-op outside Linux.
func DisableCoreDumps() error { return nil }
