//go:build !embedruntime

// Package runtime provides access to an optional embedded Kubernetes runtime.
//
// The default build does NOT embed the (large) runtime binary, so the standard
// `orcinus` binary stays small and `go build ./...` works without the gitignored
// asset. Build the embedded flavor with the `embedruntime` tag (see
// `make orcinus-embedded`).
package runtime

import "errors"

// ErrNotEmbedded is returned when the embedded runtime was not compiled in.
var ErrNotEmbedded = errors.New("embedded runtime not compiled in (build with `make orcinus-embedded`)")

// Available reports whether an embedded runtime was compiled into this binary.
func Available() bool { return false }

// ExtractPath materializes the embedded runtime binary and returns its path.
// In the default build it always fails with ErrNotEmbedded.
func ExtractPath() (string, error) { return "", ErrNotEmbedded }

// Exec would exec the embedded runtime; unavailable in the default build.
func Exec(args []string) error { return ErrNotEmbedded }
