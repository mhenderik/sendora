package app

import "strings"

// TEMPORARY STUB: delete this file when the real downloadLogAllowed
// (internal/app/download_log.go) is merged. It keeps the old behaviour of
// logging only the first request of a download (no Range, or a Range from 0).
func (s *Server) downloadLogAllowed(shareID int64, rangeHeader string) bool {
	return rangeHeader == "" || strings.HasPrefix(rangeHeader, "bytes=0-")
}
