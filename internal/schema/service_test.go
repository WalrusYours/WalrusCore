package schema

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, text string) *Schema {
	t.Helper()
	s, err := Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDiffVerdicts(t *testing.T) {
	base := exampleText(t, "feed.yml")
	cases := []struct {
		name    string
		edit    func(string) string
		verdict Verdict
	}{
		{"identical", func(s string) string { return s }, VerdictNone},
		{"weight change", func(s string) string {
			return strings.Replace(s, "default: 0.3, on: created_at", "default: 0.9, on: created_at", 1)
		}, VerdictAdditive},
		{"new interaction", func(s string) string {
			return strings.Replace(s, "interactions:\n", "interactions:\n  share:   { weight: 3.0, half_life: 7d }\n", 1)
		}, VerdictAdditive},
		{"new preset", func(s string) string {
			return strings.Replace(s, "presets:\n", "presets:\n  calm: { explore: 0.0 }\n", 1)
		}, VerdictAdditive},
		{"signal retyped", func(s string) string { return strings.Replace(s, "type: low_exposure", "type: global_count", 1) }, VerdictBreaking},
		{"interaction removed", func(s string) string {
			return strings.Replace(s, "  dislike: { weight: -5.0, half_life: 90d }\n", "", 1)
		}, VerdictBreaking},
		{"attribute retyped", func(s string) string {
			return strings.Replace(s, "topic:      { type: categorical }", "topic:      { type: string }", 1)
		}, VerdictBreaking},
	}
	old := mustParse(t, base)
	for _, c := range cases {
		edited := c.edit(base)
		if c.verdict != VerdictNone && edited == base {
			t.Fatalf("%s: edit did not change the schema", c.name)
		}
		got := Diff(old, mustParse(t, edited))
		if got.Verdict != c.verdict {
			t.Errorf("%s: verdict = %s, want %s (%v)", c.name, got.Verdict, c.verdict, got.Changes)
		}
	}
}

func TestDiffIgnoresFormattingAndKnobOrder(t *testing.T) {
	a := mustParse(t, exampleText(t, "feed.yml"))
	b := mustParse(t, exampleText(t, "feed.yml"))
	b.Knobs[0], b.Knobs[1] = b.Knobs[1], b.Knobs[0]
	if d := Diff(a, b); d.Verdict != VerdictNone {
		t.Errorf("reordering knobs should not be a change: %v", d.Changes)
	}
	if Hash(a) != Hash(b) {
		t.Error("hash should not depend on knob order")
	}
}

func TestHashChangesWithContent(t *testing.T) {
	base := exampleText(t, "feed.yml")
	a := Hash(mustParse(t, base))
	b := Hash(mustParse(t, strings.Replace(base, "default: 0.5", "default: 0.6", 1)))
	if a == b {
		t.Error("hash should change when a weight changes")
	}
}

func TestServiceLifecycle(t *testing.T) {
	svc := NewService()
	feed := []byte(exampleText(t, "feed.yml"))

	if _, ok := svc.Current(); ok {
		t.Fatal("no schema yet")
	}

	r := svc.Load(feed, LoadOptions{DryRun: true})
	if !r.OK || r.Version != 0 {
		t.Fatalf("dry run = %+v", r)
	}
	if _, ok := svc.Current(); ok {
		t.Fatal("a dry run must not activate anything")
	}

	r = svc.Load(feed, LoadOptions{Author: "ci"})
	if !r.OK || r.Version != 1 {
		t.Fatalf("first load = %+v", r)
	}

	r = svc.Load(feed, LoadOptions{})
	if !r.OK || r.Version != 1 || r.Diff.Verdict != VerdictNone {
		t.Fatalf("unchanged load should not create a version: %+v", r)
	}

	tweaked := []byte(strings.Replace(string(feed), "default: 0.3, on: created_at", "default: 0.8, on: created_at", 1))
	r = svc.Load(tweaked, LoadOptions{})
	if !r.OK || r.Version != 2 || r.Diff.Verdict != VerdictAdditive {
		t.Fatalf("additive load = %+v", r)
	}

	breaking := []byte(strings.Replace(string(tweaked), "  dislike: { weight: -5.0, half_life: 90d }\n", "", 1))
	breaking = []byte(strings.Replace(string(breaking), "- exclude: { interacted: [dislike] }\n", "", 1))
	r = svc.Load(breaking, LoadOptions{})
	if r.OK || !r.NeedsConfirm || len(r.Errors) != 0 {
		t.Fatalf("breaking load without confirmation = %+v", r)
	}
	if cur, _ := svc.Current(); cur.Version != 2 {
		t.Fatalf("a refused breaking change must not activate: v%d", cur.Version)
	}
	r = svc.Load(breaking, LoadOptions{ConfirmBreaking: true})
	if !r.OK || r.Version != 3 {
		t.Fatalf("confirmed breaking load = %+v", r)
	}

	hist := svc.History()
	if len(hist) != 3 || hist[0].Version != 3 || hist[2].Version != 1 || hist[2].Author != "ci" {
		t.Fatalf("history = %+v", hist)
	}
	if hist[0].Verdict != VerdictBreaking || hist[1].Verdict != VerdictAdditive {
		t.Errorf("history verdicts = %s, %s", hist[0].Verdict, hist[1].Verdict)
	}
}

func TestServiceRejectsInvalidAndUnparseable(t *testing.T) {
	svc := NewService()

	r := svc.Load([]byte("version: [\n"), LoadOptions{})
	if r.OK || len(r.Errors) != 1 || r.Errors[0].Message == "" {
		t.Fatalf("unparseable = %+v", r)
	}

	bad := strings.Replace(exampleText(t, "feed.yml"), "type: item_neighbors", "type: magic", 1)
	r = svc.Load([]byte(bad), LoadOptions{})
	if r.OK || len(r.Errors) == 0 || r.Errors[0].Path != "signals.content.type" {
		t.Fatalf("invalid = %+v", r)
	}
	if _, ok := svc.Current(); ok {
		t.Fatal("an invalid schema must not be activated")
	}
}
