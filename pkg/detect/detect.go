// Package detect classifies a YAML document as a Docker Compose file or a
// Kubernetes manifest. This backs `orcinus deploy` auto-detection (docs/USAGE.md §3.4).
package detect

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// Kind is the classification of a single YAML document.
type Kind string

const (
	// KindCompose is a Docker Compose document (has a top-level `services:`).
	KindCompose Kind = "compose"
	// KindManifest is a Kubernetes manifest (has `apiVersion` and `kind`).
	KindManifest Kind = "manifest"
)

// Mode is an optional forced classification supplied via `--as`.
type Mode string

const (
	// ModeAuto lets each document be detected independently (the default).
	ModeAuto Mode = ""
	// ModeCompose forces every document to be treated as compose.
	ModeCompose Mode = "compose"
	// ModeManifest forces every document to be treated as a manifest.
	ModeManifest Mode = "manifest"
)

// ParseMode validates a `--as` value.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeAuto, ModeCompose, ModeManifest:
		return Mode(s), nil
	default:
		return ModeAuto, fmt.Errorf("invalid --as value %q (want: compose|manifest)", s)
	}
}

// SplitDocuments splits a (possibly multi-document) YAML stream into individual
// non-empty documents, preserving each document's bytes.
func SplitDocuments(data []byte) ([][]byte, error) {
	r := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var docs [][]byte
	for {
		doc, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// Classify inspects a single YAML document. A forced mode short-circuits
// detection; ModeAuto applies the rules from docs/USAGE.md §3.4.
func Classify(doc []byte, force Mode) (Kind, error) {
	switch force {
	case ModeCompose:
		return KindCompose, nil
	case ModeManifest:
		return KindManifest, nil
	}

	var probe struct {
		APIVersion string                 `json:"apiVersion"`
		Kind       string                 `json:"kind"`
		Services   map[string]interface{} `json:"services"`
	}
	if err := yaml.Unmarshal(doc, &probe); err != nil {
		return "", fmt.Errorf("parse YAML document: %w", err)
	}

	switch {
	case probe.APIVersion != "" && probe.Kind != "":
		return KindManifest, nil
	case probe.Services != nil:
		return KindCompose, nil
	default:
		return "", fmt.Errorf("document is neither a compose file (no top-level `services:`) nor a k8s manifest (no `apiVersion`+`kind`)")
	}
}
