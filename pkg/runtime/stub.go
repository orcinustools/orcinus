//go:build !standalone

// Package runtime provides access to an optional standalone Kubernetes runtime.
//
// The default build does NOT embed the (large) runtime binary, so the standard
// `orcinus` binary stays small and `go build ./...` works without the gitignored
// asset. Build the standalone flavor with the `standalone` tag (see
// `make orcinus-standalone`).
package runtime

import "errors"

// ErrNotStandalone is returned when the standalone runtime was not compiled in.
var ErrNotStandalone = errors.New("standalone runtime not compiled in (build with `make orcinus-standalone`)")

// Available reports whether a standalone runtime was compiled into this binary.
func Available() bool { return false }

// ExtractPath materializes the standalone runtime binary and returns its path.
// In the default build it always fails with ErrNotStandalone.
func ExtractPath() (string, error) { return "", ErrNotStandalone }

// Exec would exec the standalone runtime; unavailable in the default build.
func Exec(args []string) error { return ErrNotStandalone }
