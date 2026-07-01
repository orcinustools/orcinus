// Package version exposes build and embedded-component version information.
package version

// These are overridden at build time via -ldflags (see the Makefile).
var (
	// Version is the orcinus release version.
	Version = "dev"
	// GitCommit is the git commit the binary was built from.
	GitCommit = "unknown"
	// KomposeRef identifies the forked kompose revision embedded in this build.
	KomposeRef = "third_party/kompose"
)
