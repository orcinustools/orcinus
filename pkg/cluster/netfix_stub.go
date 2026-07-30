//go:build !linux

package cluster

import (
	"fmt"
	"time"
)

// DisableVXLANChecksumOffload is linux-only; see netfix_linux.go.
func DisableVXLANChecksumOffload(iface string, wait time.Duration) error {
	return fmt.Errorf("netfix is only available on linux")
}
