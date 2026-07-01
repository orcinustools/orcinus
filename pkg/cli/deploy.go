package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/biznetgio/orcinus/pkg/compose"
	"github.com/biznetgio/orcinus/pkg/deploy"
	"github.com/biznetgio/orcinus/pkg/detect"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

type deployOpts struct {
	files     []string
	namespace string
	project   string
	dryRun    bool
	as        string
	output    string
	replicas  int
	pvcSize   string
}

func newDeployCmd() *cobra.Command {
	o := &deployOpts{}
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Auto-detect compose|manifest and deploy (CLI.md §3.3)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDeploy(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&o.files, "file", "f", []string{"docker-compose.yml"}, "input file (repeatable; '-' = stdin)")
	f.StringVarP(&o.namespace, "namespace", "n", "", "target namespace")
	f.StringVar(&o.project, "project", "", "ownership label (default: current directory name)")
	f.BoolVar(&o.dryRun, "dry-run", false, "render instead of applying")
	f.StringVar(&o.as, "as", "", "force input mode: compose|manifest (default: auto-detect)")
	f.StringVarP(&o.output, "output", "o", "", "also write converted manifests to this directory")
	f.IntVar(&o.replicas, "replicas", 1, "default replicas when a service specifies none")
	f.StringVar(&o.pvcSize, "pvc-size", "1Gi", "default PersistentVolumeClaim size")
	return cmd
}

func runDeploy(cmd *cobra.Command, o *deployOpts) error {
	mode, err := detect.ParseMode(o.as)
	if err != nil {
		return err
	}
	if o.project == "" {
		if wd, err := os.Getwd(); err == nil {
			o.project = filepath.Base(wd)
		}
	}

	// Read every source and split into individual YAML documents, classifying
	// each as compose or manifest.
	var composeDocs [][]byte
	var manifestObjs []runtime.Object

	for _, src := range o.files {
		raw, err := readSource(src, cmd.InOrStdin())
		if err != nil {
			return err
		}
		docs, err := detect.SplitDocuments(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}
		for _, doc := range docs {
			kind, err := detect.Classify(doc, mode)
			if err != nil {
				return fmt.Errorf("%s: %w", src, err)
			}
			switch kind {
			case detect.KindCompose:
				composeDocs = append(composeDocs, doc)
			case detect.KindManifest:
				obj, err := decodeManifest(doc)
				if err != nil {
					return fmt.Errorf("%s: %w", src, err)
				}
				manifestObjs = append(manifestObjs, obj)
			}
		}
	}

	objects := manifestObjs

	// Convert the compose documents (if any) through the forked kompose engine.
	if len(composeDocs) > 0 {
		tmpDir, err := os.MkdirTemp("", "orcinus-deploy-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmpDir)
		var files []string
		for i, doc := range composeDocs {
			p := filepath.Join(tmpDir, fmt.Sprintf("compose-%02d.yml", i))
			if err := os.WriteFile(p, doc, 0o600); err != nil {
				return err
			}
			files = append(files, p)
		}
		converted, err := compose.Convert(compose.Options{
			Files:       files,
			ProjectName: o.project,
			Namespace:   o.namespace,
			Replicas:    o.replicas,
			PVCSize:     o.pvcSize,
		})
		if err != nil {
			return err
		}
		objects = append(objects, converted...)
	}

	if len(objects) == 0 {
		return fmt.Errorf("no compose services or manifests found in input")
	}

	if o.output != "" {
		if err := deploy.WriteDir(objects, o.output); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "wrote %d object(s) to %s\n", len(objects), o.output)
	}

	if o.dryRun {
		out, err := deploy.Render(objects)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(out)
		return err
	}

	// Applying to a live cluster is M2 (client-go apply). Until then, guide the
	// user to the fully-working conversion path.
	return fmt.Errorf("applying to a cluster is not implemented yet (M2); use --dry-run [-o dir] to render manifests")
}

func readSource(src string, stdin io.Reader) ([]byte, error) {
	if src == "-" {
		return io.ReadAll(stdin)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", src, err)
	}
	return b, nil
}

// decodeManifest turns a raw k8s YAML document into an unstructured object.
func decodeManifest(doc []byte) (runtime.Object, error) {
	m := map[string]interface{}{}
	if err := yaml.Unmarshal(doc, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &unstructured.Unstructured{Object: m}, nil
}
