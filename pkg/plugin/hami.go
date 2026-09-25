package plugin

import (
	_ "embed"

	"github.com/orcinustools/orcinus/pkg/deploy"
)

// hamiYAML is the upstream HAMi chart rendered once — see the header of the file
// for the exact `helm template` command.
//
//go:embed assets/hami.yaml
var hamiYAML []byte

func buildHAMi(Options) (built, error) {
	objs, err := deploy.DecodeManifests(hamiYAML)
	return built{
		Objects: objs,
		WaitFor: []WaitTarget{{Namespace: "kube-system", Name: "hami-scheduler"}},
	}, err
}

// exclusive lists plugins that must not be installed together: HAMi and the
// NVIDIA device plugin both register nvidia.com/gpu on the same nodes.
var exclusive = map[string]string{
	"hami":                 "nvidia-device-plugin",
	"nvidia-device-plugin": "hami",
}
