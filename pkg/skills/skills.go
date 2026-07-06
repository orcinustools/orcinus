// Package skills exposes orcinus's built-in, agent-oriented usage recipes so any
// AI agent can learn to drive the CLI by running `orcinus skills` — no prior
// knowledge required. The cards are embedded, so they stay in sync with the binary.
package skills

import (
	"embed"
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

//go:embed cards/*.md
var cardFS embed.FS

// Card is one task-oriented recipe.
type Card struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Danger      bool     `json:"danger,omitempty"` // contains destructive commands
	Body        string   `json:"body"`
}

var cards = mustLoad()

func mustLoad() []Card {
	entries, err := cardFS.ReadDir("cards")
	if err != nil {
		panic("skills: " + err.Error())
	}
	var out []Card
	for _, e := range entries {
		b, err := cardFS.ReadFile("cards/" + e.Name())
		if err != nil {
			panic("skills: " + err.Error())
		}
		c, err := parseCard(b)
		if err != nil {
			panic(fmt.Sprintf("skills: %s: %v", e.Name(), err))
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func parseCard(b []byte) (Card, error) {
	s := strings.TrimPrefix(string(b), "---\n")
	parts := strings.SplitN(s, "\n---\n", 2)
	var c Card
	if len(parts) == 2 {
		if err := yaml.Unmarshal([]byte(parts[0]), &c); err != nil {
			return Card{}, err
		}
		c.Body = strings.TrimSpace(parts[1])
	} else {
		c.Body = strings.TrimSpace(s)
	}
	if c.Name == "" {
		return Card{}, fmt.Errorf("missing name in frontmatter")
	}
	return c, nil
}

// List returns all cards, sorted by name.
func List() []Card { return cards }

// Get returns a card by name.
func Get(name string) (Card, bool) {
	for _, c := range cards {
		if c.Name == name {
			return c, true
		}
	}
	return Card{}, false
}

// All renders every card into one document (the "read once, learn everything" form).
func All() string {
	var b strings.Builder
	b.WriteString("# Orcinus — agent skill catalog\n\n")
	b.WriteString("Run `orcinus skills <name>` for any single recipe. Commands that change ")
	b.WriteString("the cluster are marked (danger); prefer `orcinus deploy --dry-run` first.\n\n")
	for _, c := range cards {
		fmt.Fprintf(&b, "## %s — %s\n\n%s\n\n", c.Name, c.Description, c.Body)
	}
	return strings.TrimSpace(b.String()) + "\n"
}
