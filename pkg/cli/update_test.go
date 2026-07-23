package cli

import "testing"

func TestReleaseAsset(t *testing.T) {
	tests := []struct {
		version, goos, goarch string
		standalone            bool
		wantArchive, wantBin  string
	}{
		{"2.3.0", "darwin", "arm64", false, "orcinus_2.3.0_darwin_arm64.tar.gz", "orcinus"},
		{"v2.3.0", "linux", "amd64", false, "orcinus_2.3.0_linux_amd64.tar.gz", "orcinus"},
		{"2.3.0", "linux", "amd64", true, "orcinus-standalone_2.3.0_linux_amd64.tar.gz", "orcinus-standalone"},
	}
	for _, tt := range tests {
		got := releaseAsset(tt.version, tt.goos, tt.goarch, tt.standalone)
		if got.archive != tt.wantArchive || got.binary != tt.wantBin {
			t.Errorf("releaseAsset(%q,%q,%q,%v) = %+v, want {%q %q}",
				tt.version, tt.goos, tt.goarch, tt.standalone, got, tt.wantArchive, tt.wantBin)
		}
	}
}

func TestVersionNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"2.3.0", "2.2.0", true},
		{"v2.3.0", "v2.3.0", false},
		{"2.2.9", "2.3.0", false},
		{"2.10.0", "2.9.0", true},
		{"2.3.1", "2.3.0", true},
		{"2.3.0", "dev", true}, // dev parses as 0.0.0 -> always behind
		{"2.3.0-snapshot", "2.3.0", false},
	}
	for _, tt := range tests {
		if got := versionNewer(tt.latest, tt.current); got != tt.want {
			t.Errorf("versionNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func TestStripV(t *testing.T) {
	for in, want := range map[string]string{"v2.3.0": "2.3.0", "2.3.0": "2.3.0", " v1.0.0 ": "1.0.0"} {
		if got := stripV(in); got != want {
			t.Errorf("stripV(%q) = %q, want %q", in, got, want)
		}
	}
}
