package app

import (
	"errors"
	"io"
	"net/http"
	"time"
)

func (s *Server) transferTimeout() time.Duration {
	if s.transferIdleTimeout > 0 {
		return s.transferIdleTimeout
	}
	return 60 * time.Second
}

// Chunk uploads may run for minutes, but must keep making read progress.
type idleRequestBody struct {
	io.ReadCloser
	controller *http.ResponseController
	timeout    time.Duration
}

func (b *idleRequestBody) Read(p []byte) (int, error) {
	if err := b.controller.SetReadDeadline(time.Now().Add(b.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	return b.ReadCloser.Read(p)
}

// No ReaderFrom method: io.Copy must pass bounded chunks through Write so a
// continuously progressing large download is not subject to a total time cap.
type idleResponseWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
}

func (w *idleResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *idleResponseWriter) WriteHeader(code int) {
	_ = w.controller.SetWriteDeadline(time.Now().Add(w.timeout))
	w.ResponseWriter.WriteHeader(code)
}

func (w *idleResponseWriter) Write(p []byte) (int, error) {
	if err := w.controller.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	return w.ResponseWriter.Write(p)
}
