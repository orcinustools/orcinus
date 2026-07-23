package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/version"
)

const (
	updateRepo    = "orcinustools/orcinus"
	updateTimeout = 60 * time.Second
)

// newUpdateCmd self-updates the orcinus binary in place: it resolves the path of
// the *currently running* binary (following symlinks), downloads the matching
// release asset from GitHub, and replaces it — detecting automatically whether
// writing to that location needs sudo.
func newUpdateCmd() *cobra.Command {
	var versionFlag string
	var checkOnly, force, standalone bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the orcinus binary in place (self-update)",
		Long: "Update the orcinus binary in place.\n\n" +
			"By default it updates the currently running binary at its own location " +
			"(resolving symlinks first), to the latest GitHub release. If that location " +
			"is not writable, it detects this and re-runs the install step with sudo.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(cmd, updateOptions{
				version:    versionFlag,
				checkOnly:  checkOnly,
				force:      force,
				standalone: standalone,
			})
		},
	}
	cmd.Flags().StringVar(&versionFlag, "version", "", "version to install (default: latest release)")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "only check for a newer version, don't install")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if already on the target version")
	cmd.Flags().BoolVar(&standalone, "standalone", false, "install the standalone build (runtime embedded); auto-detected from the binary name")
	return cmd
}

type updateOptions struct {
	version    string
	checkOnly  bool
	force      bool
	standalone bool
}

func runUpdate(cmd *cobra.Command, opts updateOptions) error {
	out := cmd.OutOrStdout()
	ctx, cancel := context.WithTimeout(cmd.Context(), updateTimeout)
	defer cancel()

	// 1. Resolve where the running binary actually lives (follow symlinks).
	exe, err := currentBinaryPath()
	if err != nil {
		return err
	}
	standalone := opts.standalone || strings.Contains(filepath.Base(exe), "standalone")
	fmt.Fprintf(out, "Current binary: %s (%s)\n", exe, version.Version)

	// 2. Find the target release.
	rel, err := fetchRelease(ctx, opts.version)
	if err != nil {
		return err
	}
	target := stripV(rel.TagName)
	current := stripV(version.Version)

	upToDate := current != "dev" && current != "" && !versionNewer(target, current)
	if upToDate && !opts.force {
		fmt.Fprintf(out, "Already up to date (%s).\n", versionLabel(current))
		return nil
	}
	if opts.checkOnly {
		fmt.Fprintf(out, "Update available: %s -> %s\n", versionLabel(current), versionLabel(target))
		return nil
	}

	// 3. Pick the asset for this OS/arch and download+extract the binary.
	info := releaseAsset(target, runtime.GOOS, runtime.GOARCH, standalone)
	url, ok := rel.assetURL(info.archive)
	if !ok {
		return fmt.Errorf("release v%s has no asset %q (os=%s arch=%s)", target, info.archive, runtime.GOOS, runtime.GOARCH)
	}
	fmt.Fprintf(out, "Downloading %s ...\n", info.archive)
	staged, err := downloadAndExtract(ctx, url, info.binary)
	if err != nil {
		return err
	}
	defer os.Remove(staged)

	// 4. Install, detecting whether the target location needs elevated rights.
	if writableDir(filepath.Dir(exe)) {
		if err := replaceBinary(staged, exe); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "%s is not writable by the current user; using sudo to install.\n", filepath.Dir(exe))
		if err := replaceBinarySudo(cmd, staged, exe); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "Updated orcinus to %s at %s\n", versionLabel(target), exe)
	return nil
}

// versionLabel renders a version for display: numeric versions get a "v" prefix,
// non-release markers like "dev" are shown as-is.
func versionLabel(v string) string {
	v = stripV(v)
	if v == "" || v == "dev" {
		return v
	}
	return "v" + v
}

// currentBinaryPath returns the absolute, symlink-resolved path of the running
// binary — so `update` rewrites the real file, not a symlink pointing at it.
func currentBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate current binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// --- GitHub release lookup ---------------------------------------------------

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (r ghRelease) assetURL(name string) (string, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL, true
		}
	}
	return "", false
}

func fetchRelease(ctx context.Context, ver string) (*ghRelease, error) {
	var url string
	if ver == "" {
		url = fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", updateRepo)
	} else {
		url = fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/v%s", updateRepo, stripV(ver))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "orcinus-update")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query GitHub releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("release not found (repo %s, version %q)", updateRepo, ver)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub returned %s querying releases", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("no release found")
	}
	return &rel, nil
}

// --- download & extract ------------------------------------------------------

// downloadAndExtract fetches the tar.gz at url, extracts the entry whose base
// name is binaryName into a temp file, and returns that file's path.
func downloadAndExtract(ctx context.Context, url, binaryName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "orcinus-update")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download release asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned %s", resp.Status)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != binaryName {
			continue
		}
		tmp, err := os.CreateTemp("", "orcinus-update-*")
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(tmp, tr); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", fmt.Errorf("extract binary: %w", err)
		}
		tmp.Close()
		if err := os.Chmod(tmp.Name(), 0o755); err != nil {
			os.Remove(tmp.Name())
			return "", err
		}
		return tmp.Name(), nil
	}
	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}

// --- install -----------------------------------------------------------------

// writableDir reports whether the current user can create files in dir (i.e. can
// replace a binary living there without elevated permissions).
func writableDir(dir string) bool {
	f, err := os.CreateTemp(dir, ".orcinus-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// replaceBinary atomically swaps dst with the staged binary. It copies into a
// temp file in dst's directory first (staged may be on another filesystem), then
// renames over dst — a rename replaces the file even while the old one runs.
func replaceBinary(staged, dst string) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".orcinus-new-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	src, err := os.Open(staged)
	if err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	_, copyErr := io.Copy(tmp, src)
	src.Close()
	tmp.Close()
	if copyErr != nil {
		os.Remove(tmpName)
		return fmt.Errorf("stage new binary: %w", copyErr)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", dst, err)
	}
	return nil
}

// replaceBinarySudo installs the staged binary to dst via `sudo install`, which
// prompts for the password on the terminal.
func replaceBinarySudo(cmd *cobra.Command, staged, dst string) error {
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return fmt.Errorf("%s is not writable and sudo is not available; re-run as a user who can write it, or copy the binary manually", dst)
	}
	c := exec.CommandContext(cmd.Context(), sudo, "install", "-m", "0755", staged, dst)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		return fmt.Errorf("sudo install failed: %w", err)
	}
	return nil
}

// --- pure helpers (unit-tested) ----------------------------------------------

type assetSpec struct {
	archive string // release asset (tarball) name
	binary  string // binary file name inside the tarball
}

// releaseAsset returns the tarball and inner-binary names for a version+platform,
// mirroring .goreleaser.yaml's name templates.
func releaseAsset(version, goos, goarch string, standalone bool) assetSpec {
	v := stripV(version)
	if standalone {
		return assetSpec{
			archive: fmt.Sprintf("orcinus-standalone_%s_%s_%s.tar.gz", v, goos, goarch),
			binary:  "orcinus-standalone",
		}
	}
	return assetSpec{
		archive: fmt.Sprintf("orcinus_%s_%s_%s.tar.gz", v, goos, goarch),
		binary:  "orcinus",
	}
}

func stripV(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "v")
}

// versionNewer reports whether latest is a strictly newer semver than current.
// Non-numeric or malformed parts compare as 0, so "dev" is treated as oldest.
func versionNewer(latest, current string) bool {
	l, c := parseSemver(latest), parseSemver(current)
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseSemver(s string) [3]int {
	var out [3]int
	// Drop any pre-release/build suffix (e.g. "2.3.0-snapshot").
	s = stripV(s)
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	for i, part := range strings.SplitN(s, ".", 3) {
		if i > 2 {
			break
		}
		n, _ := strconv.Atoi(part)
		out[i] = n
	}
	return out
}
