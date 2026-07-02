package plugin

import (
	"context"
	"testing"
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
