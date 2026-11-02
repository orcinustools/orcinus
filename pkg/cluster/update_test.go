package cluster

import (
	"encoding/json"
	"strings"
	"testing"
)

// A GPU server node as `docker inspect` shows it (trimmed).
const inspectFixture = `[{
 "Config": {"Image": "orcinus/k3s-nvidia:v1.31.5-k3s1", "Hostname": "6a7908878ddc",
   "Cmd": ["server", "--write-kubeconfig-mode=644"],
   "Env": ["PATH=/usr/bin", "K3S_TOKEN=t"],
   "Labels": {"orcinus.cluster": "orcinus", "org.opencontainers.image.version": "24.04"}},
 "HostConfig": {"PortBindings": {"6443/tcp": [{"HostIp": "127.0.0.1", "HostPort": "6443"}],
                                 "80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8080"}]},
   "DeviceRequests": [{"Driver": "cdi", "DeviceIDs": ["nvidia.com/gpu=all"]}],
   "RestartPolicy": {"Name": "no"}},
 "Mounts": [{"Type": "volume", "Name": "v1", "Destination": "/var/lib/rancher/k3s"}]
}]`

func TestNodeCreateArgs(t *testing.T) {
	var cs []nodeContainer
	if err := json.Unmarshal([]byte(inspectFixture), &cs); err != nil {
		t.Fatal(err)
	}
	c := &cs[0]
	if got := c.publishedPort("80"); got != 8080 {
		t.Errorf("publishedPort(80) = %d, want 8080", got)
	}
	got := strings.Join(c.createArgs("orcinus", 80, 443), " ")
	want := "create --privileged --name orcinus --hostname 6a7908878ddc --label orcinus.cluster=orcinus -e K3S_TOKEN=t " +
		"-v v1:/var/lib/rancher/k3s -p 127.0.0.1:6443:6443/tcp -p 0.0.0.0:80:80 -p 0.0.0.0:443:443 " +
		"--device nvidia.com/gpu=all orcinus/k3s-nvidia:v1.31.5-k3s1 server --write-kubeconfig-mode=644"
	if got != want {
		t.Errorf("createArgs:\n got  %s\n want %s", got, want)
	}
	// 0 drops the port instead of publishing it.
	if a := strings.Join(c.createArgs("orcinus", 0, 0), " "); strings.Contains(a, ":80") {
		t.Errorf("port 0 still published: %s", a)
	}
}

func TestUpgradeSteps(t *testing.T) {
	if got := "rancher/k3s:" + dockerTag(TargetVersion()); got != DefaultImage {
		t.Errorf("upgrade target %s != DefaultImage %s", got, DefaultImage)
	}
	for cur, want := range map[string]string{
		"v1.31.5+k3s1": "v1.32.13+k3s1 v1.33.13+k3s2 v1.34.12+k3s1 v1.35.9+k3s1 v1.36.5+k3s1",
		"v1.35.1+k3s1": "v1.35.9+k3s1 v1.36.5+k3s1",
		"v1.36.5+k3s1": "",
		"v1.36.9+k3s1": "", // newer patch: no downgrade
		"v1.37.1+k3s1": "",
	} {
		steps, err := upgradeSteps(cur)
		if err != nil || strings.Join(steps, " ") != want {
			t.Errorf("upgradeSteps(%s) = %v, %v; want %q", cur, steps, err, want)
		}
	}
	if _, err := upgradeSteps("v1.29.0+k3s1"); err == nil {
		t.Error("1.29 should be refused (too far behind)")
	}
	if fromDockerTag("v1.33.13-k3s2") != "v1.33.13+k3s2" {
		t.Error("fromDockerTag")
	}
}
