package build

import (
	"bytes"
	"fmt"

	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

// parsedDockerfile is the analyzed form of a Dockerfile: its stages plus the
// ARG declarations that appear before the first FROM (meta args). It also
// records why, if at all, the native backend cannot handle it.
type parsedDockerfile struct {
	stages   []instructions.Stage
	metaArgs []instructions.ArgCommand
	escape   rune

	// reason is set when the native backend cannot build this Dockerfile and
	// buildah must be used instead ("" means native is fine).
	reason string
}

// needsExecutor reports whether this Dockerfile requires a real build executor
// (buildah) rather than the native, no-runtime backend.
func (p *parsedDockerfile) needsExecutor() bool { return p.reason != "" }

// executorReason returns a human-readable reason why buildah is required.
func (p *parsedDockerfile) executorReason() string { return p.reason }

// finalStage returns the stage the build produces: the --target stage if named,
// otherwise the last stage in the file.
func (p *parsedDockerfile) finalStage(target string) (*instructions.Stage, error) {
	if len(p.stages) == 0 {
		return nil, fmt.Errorf("Dockerfile defines no stages")
	}
	if target == "" {
		return &p.stages[len(p.stages)-1], nil
	}
	for i := range p.stages {
		if p.stages[i].Name == target {
			return &p.stages[i], nil
		}
	}
	return nil, fmt.Errorf("target stage %q not found", target)
}

// parseDockerfile lexes and parses a Dockerfile, then classifies it: any
// instruction the native backend cannot reproduce without executing commands
// sets reason so the caller routes the build to buildah.
func parseDockerfile(data []byte, _ map[string]string) (*parsedDockerfile, error) {
	res, err := parser.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	stages, metaArgs, err := instructions.Parse(res.AST, nil)
	if err != nil {
		return nil, err
	}
	p := &parsedDockerfile{stages: stages, metaArgs: metaArgs, escape: res.EscapeToken}

	// More than one stage means COPY --from / multi-stage semantics that the
	// native backend does not model; hand it to buildah.
	if len(stages) > 1 {
		p.reason = "multi-stage build"
		return p, nil
	}
	if len(stages) == 1 {
		for _, cmd := range stages[0].Commands {
			switch c := cmd.(type) {
			case *instructions.RunCommand:
				p.reason = "RUN instruction"
				return p, nil
			case *instructions.CopyCommand:
				if c.From != "" {
					p.reason = "COPY --from another stage/image"
					return p, nil
				}
			case *instructions.HealthCheckCommand:
				// HEALTHCHECK is config-only and is handled natively below; no-op.
			case *instructions.OnbuildCommand:
				p.reason = "ONBUILD instruction"
				return p, nil
			}
		}
	}
	return p, nil
}
