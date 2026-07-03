//go:build !standalone

package cluster

import (
	"fmt"

	"github.com/orcinustools/orcinus/pkg/runtime"
)

// The default binary does not embed a runtime, so `--runtime standalone` is not
// available. Build the standalone flavor with `make orcinus-standalone`.

func initStandalone(o InitOptions) (*InitResult, error) {
	return nil, fmt.Errorf("--runtime standalone is not available: %w\n"+
		"use --runtime docker (the default), or build the standalone binary with `make orcinus-standalone`", runtime.ErrNotStandalone)
}

func downStandalone(name string) (int, error) { return 0, runtime.ErrNotStandalone }

func standaloneRunning(name string) bool { return false }

func standaloneNodes(kubeconfig string) string { return "" }

// StandaloneKubectl is unavailable in the default (non-standalone) build.
func StandaloneKubectl() (bin string, ok bool) { return "", false }
