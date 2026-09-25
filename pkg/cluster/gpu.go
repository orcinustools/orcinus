package cluster

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed gpu.Dockerfile
var gpuDockerfile []byte

// cdiGPUs is the CDI device that hands a container every NVIDIA GPU with its
// driver. Used instead of `--gpus all`, which newer docker resolves through
// vendor auto-detection and can get wrong (e.g. "AMD CDI spec not found").
const cdiGPUs = "nvidia.com/gpu=all"

const cdiHint = "GPU nodes need the NVIDIA container toolkit and its CDI spec on the host:\n" +
	"  sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml\n" +
	"(install: https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)"

// gpuImage returns the GPU node image for a k3s base image, building it from
// gpu.Dockerfile the first time. It is built locally rather than pulled so no
// extra registry is involved; the build takes a minute or two, once per host.
func gpuImage(base string) (string, error) {
	tag := base
	if i := strings.LastIndex(base, ":"); i >= 0 {
		tag = base[i+1:]
	}
	img := "orcinus/k3s-nvidia:" + tag
	if _, err := docker("image", "inspect", img); err == nil {
		return img, nil
	}
	dir, err := os.MkdirTemp("", "orcinus-gpu-image")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), gpuDockerfile, 0o644); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "building GPU node image %s (once per host)...\n", img)
	if out, err := docker("build", "-t", img, "--build-arg", "K3S_IMAGE="+base, dir); err != nil {
		return "", fmt.Errorf("build GPU node image: %w\n%s", err, out)
	}
	return img, nil
}

// checkNativeGPU is the standalone runtime's preflight: k3s registers the NVIDIA
// runtime by itself when it finds the toolkit, so the toolkit is all it needs.
func checkNativeGPU() error {
	if _, err := exec.LookPath("nvidia-container-runtime"); err != nil {
		return fmt.Errorf("--gpus: nvidia-container-runtime not found on this host\n" +
			"(install the NVIDIA container toolkit: https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)")
	}
	return nil
}
