//go:build !embedruntime

package cluster

import (
	"fmt"

	"github.com/orcinustools/orcinus/pkg/runtime"
)

// The default binary does not embed a runtime, so `--runtime embedded` is not
// available. Build the embedded flavor with `make orcinus-embedded`.

func initEmbedded(o InitOptions) (*InitResult, error) {
	return nil, fmt.Errorf("--runtime embedded is not available: %w\n"+
		"use --runtime docker (the default), or build the embedded binary with `make orcinus-embedded`", runtime.ErrNotEmbedded)
}

func downEmbedded(name string) (int, error) { return 0, runtime.ErrNotEmbedded }

func embeddedRunning(name string) bool { return false }

func embeddedNodes(kubeconfig string) string { return "" }

// EmbeddedKubectl is unavailable in the default (non-embedded) build.
func EmbeddedKubectl() (bin string, ok bool) { return "", false }
