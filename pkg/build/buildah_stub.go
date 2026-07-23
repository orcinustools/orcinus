//go:build !orcinus_buildah || !linux

package build

import (
	"context"
	"fmt"
	"runtime"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// buildWithBuildah is the stub used when the buildah backend is not compiled
// in. The native engine has no way to execute RUN steps, so we fail with an
// actionable message rather than silently producing a wrong image.
//
// The buildah backend is opt-in (like the standalone runtime): build orcinus
// with `-tags orcinus_buildah` on Linux — see docs/BUILD.md — to enable full
// `docker build` compatibility (RUN, multi-stage).
// InitReexec is a no-op in builds without the buildah backend. In the
// buildah-enabled build it initializes the containers/storage reexec machinery
// and reports whether the process was a namespace re-execution (so main can
// exit). Kept here so main() can call it unconditionally.
func InitReexec() bool { return false }

func buildWithBuildah(_ context.Context, _ Options) (v1.Image, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf(
			"this Dockerfile needs a build executor (RUN/multi-stage), which requires the buildah backend — only available on Linux; " +
				"run `orcinus build` on a Linux host, or restructure the Dockerfile to avoid RUN")
	}
	return nil, fmt.Errorf(
		"this Dockerfile needs a build executor (RUN/multi-stage); rebuild orcinus with `-tags orcinus_buildah` to enable the buildah backend (see docs/BUILD.md), " +
			"or restructure the Dockerfile so it only uses FROM/COPY/ADD/ENV/WORKDIR/CMD/ENTRYPOINT/EXPOSE/LABEL/USER")
}
