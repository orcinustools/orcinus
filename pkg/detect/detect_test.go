package detect

import "testing"

func TestClassifyAuto(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want Kind
		err  bool
	}{
		{"compose", "services:\n  web:\n    image: nginx\n", KindCompose, false},
		{"manifest", "apiVersion: v1\nkind: Service\nmetadata:\n  name: x\n", KindManifest, false},
		{"neither", "foo: bar\n", "", true},
		{"kind-without-apiversion", "kind: Service\n", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Classify([]byte(tc.doc), ModeAuto)
			if tc.err {
				if err == nil {
					t.Fatalf("expected error, got kind %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClassifyForced(t *testing.T) {
	// A compose-looking doc forced to manifest, and vice versa.
	if k, _ := Classify([]byte("services: {}"), ModeManifest); k != KindManifest {
		t.Fatalf("force manifest: got %q", k)
	}
	if k, _ := Classify([]byte("apiVersion: v1\nkind: Pod"), ModeCompose); k != KindCompose {
		t.Fatalf("force compose: got %q", k)
	}
}

func TestParseMode(t *testing.T) {
	if _, err := ParseMode("bogus"); err == nil {
		t.Fatal("expected error for bogus mode")
	}
	for _, s := range []string{"", "compose", "manifest"} {
		if _, err := ParseMode(s); err != nil {
			t.Fatalf("ParseMode(%q): %v", s, err)
		}
	}
}

func TestSplitDocuments(t *testing.T) {
	in := "apiVersion: v1\nkind: A\n---\napiVersion: v1\nkind: B\n---\n\n"
	docs, err := SplitDocuments([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}
}
