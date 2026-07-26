package plugin

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestProfilesReferenceRealPlugins guards against typos in the Profiles map.
func TestProfilesReferenceRealPlugins(t *testing.T) {
	for profile, names := range Profiles {
		for _, n := range names {
			if _, ok := Registry[n]; !ok {
				t.Errorf("profile %q references unknown plugin %q", profile, n)
			}
		}
	}
}

// TestInstallProfileUnknown fails fast (no cluster) on an unknown profile.
func TestInstallProfileUnknown(t *testing.T) {
	if err := InstallProfile(context.Background(), "does-not-exist", Options{}); err == nil {
		t.Fatal("expected an error for an unknown profile")
	}
}

// TestCertManagerDNSIssuer: DNS-01 options add a letsencrypt-dns ClusterIssuer.
func TestCertManagerDNSIssuer(t *testing.T) {
	// HTTP-01 only → just the "letsencrypt" issuer.
	objs, _ := certManagerIssuer(Options{Email: "a@b.c"})
	if len(objs) != 1 {
		t.Fatalf("http-01 only: expected 1 object, got %d", len(objs))
	}
	// With Cloudflare DNS-01 → also a token Secret + letsencrypt-dns issuer.
	objs, _ = certManagerIssuer(Options{Email: "a@b.c", DNSProvider: "cloudflare", DNSToken: "tok"})
	if len(objs) != 3 {
		t.Fatalf("dns-01: expected 3 objects, got %d", len(objs))
	}
	found := false
	for _, o := range objs {
		if u, ok := o.(*unstructured.Unstructured); ok && u.GetName() == "letsencrypt-dns" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a letsencrypt-dns ClusterIssuer")
	}
}

// TestBuildKubeVirt: the CR is a post-install object (needs the operator's CRD),
// --emulation flips useEmulation, and --cdi adds the importer.
func TestBuildKubeVirt(t *testing.T) {
	b, err := Registry["kubevirt"].Build(Options{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(b.Manifests) != 1 || len(b.PostObjects) != 1 || len(b.WaitFor) != 1 {
		t.Fatalf("default: got %d manifests, %d post objects, %d waits", len(b.Manifests), len(b.PostObjects), len(b.WaitFor))
	}
	if b.WaitFor[0] != (WaitTarget{Namespace: "kubevirt", Name: "virt-operator"}) {
		t.Errorf("unexpected wait target %+v", b.WaitFor[0])
	}
	dev := developerConfig(t, b.PostObjects[0])
	if _, ok := dev["useEmulation"]; ok {
		t.Errorf("useEmulation should be unset without --emulation")
	}

	b, _ = Registry["kubevirt"].Build(Options{Emulation: true, CDI: true})
	if dev := developerConfig(t, b.PostObjects[0]); dev["useEmulation"] != true {
		t.Errorf("--emulation: useEmulation=%v, want true", dev["useEmulation"])
	}
	if len(b.Manifests) != 2 || len(b.PostObjects) != 2 || len(b.WaitFor) != 2 {
		t.Fatalf("--cdi: got %d manifests, %d post objects, %d waits", len(b.Manifests), len(b.PostObjects), len(b.WaitFor))
	}
	if u, ok := b.PostObjects[1].(*unstructured.Unstructured); !ok || u.GetKind() != "CDI" {
		t.Errorf("--cdi: expected a CDI object, got %#v", b.PostObjects[1])
	}
}

// developerConfig digs spec.configuration.developerConfiguration out of a CR.
func developerConfig(t *testing.T, obj interface{}) map[string]interface{} {
	t.Helper()
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		t.Fatalf("expected an unstructured object, got %#v", obj)
	}
	dev, found, err := unstructured.NestedMap(u.Object, "spec", "configuration", "developerConfiguration")
	if err != nil || !found {
		t.Fatalf("developerConfiguration not found (err=%v)", err)
	}
	return dev
}

// TestResolveStorageProviders sanity-checks the storage provider variants build.
func TestResolveStorageProviders(t *testing.T) {
	spec := Registry["storage"]
	cases := []struct {
		o       Options
		wantErr bool
	}{
		{Options{Provider: "minio", Replicas: 4}, false},
		{Options{Provider: "longhorn", Replicas: 3}, false},
		{Options{Provider: "rook-ceph", CephFailureDomain: "osd"}, false},
		{Options{Provider: "nfs"}, true},              // missing server/path
		{Options{Provider: "bogus"}, true},
	}
	for _, c := range cases {
		_, err := spec.Build(c.o)
		if (err != nil) != c.wantErr {
			t.Errorf("provider %q: err=%v wantErr=%v", c.o.Provider, err, c.wantErr)
		}
	}
}
