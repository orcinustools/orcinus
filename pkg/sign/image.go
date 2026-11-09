package sign

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const (
	payloadType   = "application/vnd.dev.cosign.simplesigning.v1+json"
	sigAnnotation = "dev.cosignproject.cosign/signature"
	signatureType = "cosign container image signature"
)

// ErrNotSigned means the image has no signature from any of the given keys.
var ErrNotSigned = errors.New("not signed by a trusted key")

// payload is cosign's "simple signing" document.
type payload struct {
	Critical struct {
		Identity struct {
			DockerReference string `json:"docker-reference"`
		} `json:"identity"`
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
		Type string `json:"type"`
	} `json:"critical"`
	Optional map[string]any `json:"optional"`
}

// Options reach the registry. Credentials come from the docker config
// (~/.docker/config.json, `docker login`).
type Options struct {
	Insecure bool // plain-HTTP registry
}

func (o Options) remote(ctx context.Context) []remote.Option {
	return []remote.Option{remote.WithAuthFromKeychain(authn.DefaultKeychain), remote.WithContext(ctx)}
}

func (o Options) parse(ref string) (name.Reference, error) {
	var opts []name.Option
	if o.Insecure {
		opts = append(opts, name.Insecure)
	}
	return name.ParseReference(ref, opts...)
}

// Resolve returns the immutable form of an image reference: repo@sha256:….
func Resolve(ctx context.Context, ref string, o Options) (name.Digest, error) {
	r, err := o.parse(ref)
	if err != nil {
		return name.Digest{}, err
	}
	if d, ok := r.(name.Digest); ok {
		return d, nil
	}
	desc, err := remote.Head(r, o.remote(ctx)...)
	if err != nil {
		return name.Digest{}, fmt.Errorf("resolve %s: %w", ref, err)
	}
	return r.Context().Digest(desc.Digest.String()), nil
}

func sigTag(d name.Digest) name.Tag {
	return d.Context().Tag(strings.Replace(d.DigestStr(), ":", "-", 1) + ".sig")
}

// SignImage signs the image ref currently points to and pushes the signature.
// Signing again with the same key is a no-op. It returns the signed digest.
func SignImage(ctx context.Context, ref string, key *ecdsa.PrivateKey, o Options) (name.Digest, error) {
	d, err := Resolve(ctx, ref, o)
	if err != nil {
		return d, err
	}
	base, err := remote.Image(sigTag(d), o.remote(ctx)...)
	if err != nil {
		if !isNotFound(err) {
			return d, fmt.Errorf("read existing signatures: %w", err)
		}
		base = mutate.ConfigMediaType(mutate.MediaType(empty.Image, types.OCIManifestSchema1), types.OCIConfigJSON)
	} else if _, err := verifyIn(base, d, map[string]*ecdsa.PublicKey{"": &key.PublicKey}); err == nil {
		return d, nil // already signed by this key
	}

	var p payload
	p.Critical.Identity.DockerReference = d.Context().Name()
	p.Critical.Image.DockerManifestDigest = d.DigestStr()
	p.Critical.Type = signatureType
	body, err := json.Marshal(p)
	if err != nil {
		return d, err
	}
	sum := sha256.Sum256(body)
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		return d, err
	}
	img, err := mutate.Append(base, mutate.Addendum{
		Layer:       static.NewLayer(body, payloadType),
		Annotations: map[string]string{sigAnnotation: base64.StdEncoding.EncodeToString(sig)},
	})
	if err != nil {
		return d, err
	}
	if err := remote.Write(sigTag(d), img, o.remote(ctx)...); err != nil {
		return d, fmt.Errorf("push signature: %w", err)
	}
	return d, nil
}

// Verify checks that ref carries a signature from one of keys (name → key)
// and returns the verified digest and the name of the key that signed it.
func Verify(ctx context.Context, ref string, keys map[string]*ecdsa.PublicKey, o Options) (name.Digest, string, error) {
	d, err := Resolve(ctx, ref, o)
	if err != nil {
		return d, "", err
	}
	img, err := remote.Image(sigTag(d), o.remote(ctx)...)
	if err != nil {
		if isNotFound(err) {
			return d, "", fmt.Errorf("%s: %w (no signatures)", d, ErrNotSigned)
		}
		return d, "", fmt.Errorf("read signatures of %s: %w", d, err)
	}
	signer, err := verifyIn(img, d, keys)
	if err != nil {
		return d, "", fmt.Errorf("%s: %w", d, err)
	}
	return d, signer, nil
}

// verifyIn looks for a layer of the signature image whose payload names d and
// whose signature checks out against one of keys.
func verifyIn(img v1.Image, d name.Digest, keys map[string]*ecdsa.PublicKey) (string, error) {
	m, err := img.Manifest()
	if err != nil {
		return "", err
	}
	layers, err := img.Layers()
	if err != nil {
		return "", err
	}
	for i, desc := range m.Layers {
		if desc.MediaType != payloadType || i >= len(layers) {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(desc.Annotations[sigAnnotation])
		if err != nil || len(sig) == 0 {
			continue
		}
		rc, err := layers[i].Uncompressed()
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(rc, 1<<20))
		rc.Close()
		if err != nil {
			continue
		}
		var p payload
		if json.Unmarshal(body, &p) != nil || p.Critical.Image.DockerManifestDigest != d.DigestStr() {
			continue
		}
		sum := sha256.Sum256(body)
		for n, k := range keys {
			if ecdsa.VerifyASN1(k, sum[:], sig) {
				return n, nil
			}
		}
	}
	return "", ErrNotSigned
}

func isNotFound(err error) bool {
	var te *transport.Error
	return errors.As(err, &te) && te.StatusCode == 404
}
