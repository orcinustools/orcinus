package sign

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func newKey(t *testing.T, pass string) (*ecdsa.PrivateKey, *ecdsa.PublicKey) {
	t.Helper()
	priv, pub, err := GenerateKey([]byte(pass))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePrivateKey(priv, []byte("wrong"+pass)); err == nil {
		t.Fatal("wrong password accepted")
	}
	k, err := ParsePrivateKey(priv, []byte(pass))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if !k.PublicKey.Equal(p) {
		t.Fatal("public key does not match private key")
	}
	return k, p
}

func TestSignVerify(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	ctx, o := context.Background(), Options{Insecure: true}
	ref := strings.TrimPrefix(srv.URL, "http://") + "/app:1.0"
	img, _ := random.Image(64, 1)
	tag, _ := name.ParseReference(ref, name.Insecure)
	if err := remote.Write(tag, img, remote.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}

	alice, alicePub := newKey(t, "pw")
	bob, bobPub := newKey(t, "")

	if _, _, err := Verify(ctx, ref, map[string]*ecdsa.PublicKey{"alice": alicePub}, o); !errors.Is(err, ErrNotSigned) {
		t.Fatalf("unsigned image: err = %v, want ErrNotSigned", err)
	}
	d, err := SignImage(ctx, ref, alice, o)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := img.Digest()
	if d.DigestStr() != want.String() {
		t.Fatalf("signed %s, want %s", d.DigestStr(), want)
	}
	if _, err := SignImage(ctx, ref, alice, o); err != nil { // idempotent
		t.Fatal(err)
	}
	if _, _, err := Verify(ctx, ref, map[string]*ecdsa.PublicKey{"bob": bobPub}, o); !errors.Is(err, ErrNotSigned) {
		t.Fatalf("other key: err = %v, want ErrNotSigned", err)
	}
	if _, err := SignImage(ctx, ref, bob, o); err != nil {
		t.Fatal(err)
	}
	for who, k := range map[string]*ecdsa.PublicKey{"alice": alicePub, "bob": bobPub} {
		_, signer, err := Verify(ctx, d.String(), map[string]*ecdsa.PublicKey{who: k}, o)
		if err != nil || signer != who {
			t.Fatalf("verify %s: signer %q, err %v", who, signer, err)
		}
	}
	sigs, err := remote.Image(sigTag(d))
	if err != nil {
		t.Fatal(err)
	}
	if ls, _ := sigs.Layers(); len(ls) != 2 {
		t.Fatalf("signature layers = %d, want 2 (alice once, bob once)", len(ls))
	}

	// A moved tag is a different digest: the old signature does not cover it.
	img2, _ := random.Image(64, 1)
	if err := remote.Write(tag, img2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Verify(ctx, ref, map[string]*ecdsa.PublicKey{"alice": alicePub}, o); !errors.Is(err, ErrNotSigned) {
		t.Fatalf("retagged image: err = %v, want ErrNotSigned", err)
	}
}
