//go:build embedruntime

// Package runtime provides access to an optional embedded Kubernetes runtime.
//
// This file is compiled only with the `embedruntime` build tag; it uses
// go:embed to bundle the runtime binary into the orcinus binary. The default
// build uses stub.go instead, so the standard binary stays small.
package runtime

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

//go:embed assets/k3s
var runtimeBinary []byte

// Available reports whether an embedded runtime was compiled into this binary.
func Available() bool { return true }

// ExtractPath writes the embedded runtime to a stable location (once) and
// returns its path. The directory is overridable via ORCINUS_RUNTIME_DIR.
func ExtractPath() (string, error) {
	dir := "/var/lib/orcinus/bin"
	if v := os.Getenv("ORCINUS_RUNTIME_DIR"); v != "" {
		dir = v
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "k3s")
	if fi, err := os.Stat(path); err == nil && fi.Size() == int64(len(runtimeBinary)) {
		return path, nil // already extracted, same size
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, runtimeBinary, 0o755); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

// Exec extracts the embedded runtime and execs it with args, replacing the
// current process (the runtime binary is multicall: `<bin> server|kubectl|...`).
// It only returns on failure. ETXTBSY is retried (write-then-exec race).
func Exec(args []string) error {
	path, err := ExtractPath()
	if err != nil {
		return err
	}
	argv := append([]string{path}, args...)
	env := os.Environ()
	for attempt := 0; attempt < 100; attempt++ {
		err := syscall.Exec(path, argv, env) // only returns on failure
		if errors.Is(err, syscall.ETXTBSY) {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		return fmt.Errorf("exec runtime: %w", err)
	}
	return fmt.Errorf("exec runtime: still busy after retries")
}
