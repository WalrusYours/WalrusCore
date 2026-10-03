package schema

import (
	"strings"
	"testing"
)

func TestLockedParsesOnEveryKind(t *testing.T) {
	s := exampleText(t, "feed.yml")
	edits := []struct{ old, new string }{
		{"dislike: { weight: -5.0, half_life: 90d }", "dislike: { weight: -5.0, half_life: 90d, locked: true }"},
		{"exploration:   { type: low_exposure,     default: 0.1 }", "exploration:   { type: low_exposure,     default: 0.1, locked: true }"},
		{"- { on: topic, metric: equals,  weight: 0.5 }", "- { on: topic, metric: equals,  weight: 0.5, locked: true }"},
		{"  - id: horizon\n", "  - id: horizon\n    locked: true\n"},
	}
	for _, e := range edits {
		if !strings.Contains(s, e.old) {
			t.Fatalf("setup: %q not found in feed.yml", e.old)
		}
		s = strings.Replace(s, e.old, e.new, 1)
	}

	parsed := mustParse(t, s)
	if !parsed.Interactions["dislike"].Locked || parsed.Interactions["view"].Locked {
		t.Error("interaction lock")
	}
	if !parsed.Signals["exploration"].Locked || parsed.Signals["content"].Locked {
		t.Error("signal lock")
	}
	if _, leaked := parsed.Signals["exploration"].Params["locked"]; leaked {
		t.Error("locked must not leak into signal Params")
	}
	if !parsed.Similarity["post"][0].Locked || parsed.Similarity["post"][1].Locked {
		t.Error("similarity term lock")
	}
	if k, _ := parsed.Knob("horizon"); !k.Locked {
		t.Error("knob lock")
	}
	if issues := Validate(parsed); len(issues) != 0 {
		t.Errorf("a schema with locks should validate: %v", issues)
	}
}

// A lock only guards the editors. The engine accepts a change to a locked value, because
// unlocking and editing must work in a single deploy.
func TestServiceDoesNotRejectChangesToLockedValues(t *testing.T) {
	svc := NewService()
	locked := strings.Replace(exampleText(t, "feed.yml"), "dislike: { weight: -5.0, half_life: 90d }", "dislike: { weight: -5.0, half_life: 90d, locked: true }", 1)
	if r := svc.Load([]byte(locked), LoadOptions{}); !r.OK || r.Version != 1 {
		t.Fatalf("first push = %+v", r)
	}
	edited := strings.Replace(locked, "dislike: { weight: -5.0", "dislike: { weight: -2.0", 1)
	if r := svc.Load([]byte(edited), LoadOptions{}); !r.OK || r.Version != 2 {
		t.Fatalf("editing a locked value should be accepted: %+v", r)
	}
}

func TestNewsExampleShowsLocks(t *testing.T) {
	s := load(t, "news.yml")
	if !s.Interactions["hide"].Locked || !s.Signals["exploration"].Locked {
		t.Error("news.yml should demonstrate locked values")
	}
}
