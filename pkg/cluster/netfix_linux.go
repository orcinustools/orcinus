//go:build linux

package cluster

import (
	"fmt"
	"net"
	"time"

	"github.com/safchain/ethtool"
)

// DisableVXLANChecksumOffload waits for iface to appear (flannel creates it
// once the node is up) and turns off its TX checksum offload. VXLAN packets
// with offloaded inner checksums are corrupted when the outer UDP traverses
// NAT — which is exactly what happens when a node container publishes
// 8472/udp on its host. Symptom without this: cross-node ICMP works but every
// cross-node TCP connection hangs.
//
// It runs INSIDE the node container (via the hidden `cluster netfix`
// command), because the k3s image ships no ethtool.
func DisableVXLANChecksumOffload(iface string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		if _, err := net.InterfaceByName(iface); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("interface %q did not appear within %s", iface, wait)
		}
		time.Sleep(2 * time.Second)
	}
	et, err := ethtool.NewEthtool()
	if err != nil {
		return fmt.Errorf("ethtool: %w", err)
	}
	defer et.Close()
	if err := et.Change(iface, map[string]bool{"tx-checksum-ip-generic": false}); err != nil {
		return fmt.Errorf("disable tx-checksum-ip-generic on %s: %w", iface, err)
	}
	return nil
}
