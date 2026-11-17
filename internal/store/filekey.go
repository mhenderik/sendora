package store

// FileKey is the master key for encrypting stored share files, derived from the
// database key. It is nil for the plaintext fixture store, which then stores
// share files unencrypted. Callers must not modify the slice.
func (s *Store) FileKey() []byte { return s.fileKey }
