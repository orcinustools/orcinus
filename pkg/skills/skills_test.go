package skills

import "testing"

func TestCardsLoad(t *testing.T) {
	cards := List()
	if len(cards) < 5 {
		t.Fatalf("expected several cards, got %d", len(cards))
	}
	for _, c := range cards {
		if c.Name == "" || c.Description == "" || c.Body == "" {
			t.Errorf("card %q incomplete: %+v", c.Name, c)
		}
	}
	if _, ok := Get("overview"); !ok {
		t.Error("missing 'overview' card")
	}
	if _, ok := Get("nope"); ok {
		t.Error("Get returned a nonexistent card")
	}
}

func TestAll(t *testing.T) {
	all := All()
	if len(all) < 200 || !contains(all, "orcinus deploy") {
		t.Errorf("All() looks empty/incomplete (%d bytes)", len(all))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
