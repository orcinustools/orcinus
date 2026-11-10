package plugin

import (
	_ "embed"

	"github.com/orcinustools/orcinus/pkg/deploy"
)

// tetragonYAML is the upstream Tetragon chart rendered once — see the header of
// the file for the exact command.
//
//go:embed assets/tetragon.yaml
var tetragonYAML []byte

//go:embed assets/tetragon-files.yaml
var tetragonFilesYAML []byte

// TetragonNamespace is where the agents run; `orcinus monitor` reads their
// event export from there.
const TetragonNamespace = "tetragon"

func buildTetragon(Options) (built, error) {
	objs, err := deploy.DecodeManifests(tetragonYAML)
	if err != nil {
		return built{}, err
	}
	// The operator registers the TracingPolicy CRD on start, so the file policy
	// goes in after it is up.
	post, err := deploy.DecodeManifests(tetragonFilesYAML)
	return built{
		Namespace:   TetragonNamespace,
		Objects:     objs,
		WaitFor:     []WaitTarget{{Namespace: TetragonNamespace, Name: "tetragon-operator"}},
		PostObjects: post,
	}, err
}
