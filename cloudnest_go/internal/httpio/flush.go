// Package httpio preserves flush errors across ResponseWriter adapters.
package httpio

import "net/http"

// Flush prefers an error-aware wrapper, then Unwrap, before a legacy Flusher.
// In particular Gin's Flush discards the underlying transport's error. Wrappers
// with an access policy must implement FlushError themselves (as blob leases do).
func Flush(w http.ResponseWriter) error {
	for {
		switch writer := w.(type) {
		case interface{ FlushError() error }:
			return writer.FlushError()
		case interface{ Unwrap() http.ResponseWriter }:
			w = writer.Unwrap()
		case http.Flusher:
			writer.Flush()
			return nil
		default:
			return http.ErrNotSupported
		}
	}
}
