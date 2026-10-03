package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exampleText(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "schema", "schema-examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func issuesFor(t *testing.T, text string) []Issue {
	t.Helper()
	s, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return Validate(s)
}

func TestShippedExamplesValidate(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "docs", "schema", "schema-examples", "*.yml"))
	if len(files) < 6 {
		t.Fatalf("found %d examples", len(files))
	}
	for _, f := range files {
		name := filepath.Base(f)
		if issues := issuesFor(t, exampleText(t, name)); len(issues) != 0 {
			t.Errorf("%s: %v", name, issues)
		}
	}
}

// Each case edits the marketplace example so exactly one rule is broken, and expects an
// issue at the given path containing the given text.
func TestValidateCatchesMistakes(t *testing.T) {
	base := exampleText(t, "marketplace.yml")
	cases := []struct {
		name, old, new, path, contains string
	}{
		{"bad version", "version: 1", "version: 0", "version", "at least 1"},
		{"bad entity name", "  seller:\n    key: id", "  Seller:\n    key: id", "entities.Seller", "must match"},
		{"unknown attribute type", "rating: { type: float, range: [0, 5] }", "rating: { type: number }", "entities.seller.attributes.rating.type", "unknown type"},
		{"ref to nowhere", "type: ref, entity: seller", "type: ref, entity: vendor", "entities.listing.attributes.seller_id.entity", "declared entity"},
		{"bad range", "range: [0, 5]", "range: [5, 0]", "entities.seller.attributes.rating.range", "min < max"},
		{"range on a string", "condition:   { type: categorical }", "condition:   { type: categorical, range: [0, 1] }", "entities.listing.attributes.condition.range", "only to float"},
		{"missing user entity", "  user:\n    key: id", "  buyer:\n    key: id", "entities.user", "required"},
		{"interaction target", "favourite: { weight: 4.0,  half_life: 30d }", "favourite: { weight: 4.0, target: nope }", "interactions.favourite.target", "declared entity"},
		{"transform without value", "favourite: { weight: 4.0,  half_life: 30d }", "favourite: { weight: 4.0, transform: log1p }", "interactions.favourite.transform", "needs a value"},
		{"bad transform", "transform: log1p", "transform: nonsense(", "interactions.view.transform", "unknown transform"},
		{"similarity entity", "similarity:\n  listing:", "similarity:\n  gadget:", "similarity.gadget", "declared entity"},
		{"similarity attribute", "- { on: category,       metric: equals,    weight: 0.25 }", "- { on: categry,        metric: equals,    weight: 0.25 }", "similarity.listing[0].on", "not an attribute"},
		{"similarity metric", "metric: jaccard", "metric: manhattan", "similarity.listing[1].metric", "metric must be"},
		{"signal type", "type: item_neighbors", "type: magic", "signals.content.type", "unknown signal type"},
		{"signal window", "window: 7d", "window: soon", "signals.popularity.window", "duration"},
		{"age_decay on a non-timestamp", "on: created_at, half_life: 5d", "on: city, half_life: 5d", "signals.recency.on", "timestamp"},
		{"signal on unknown attribute", "on: seller_id, default: 0.3", "on: sellr_id, default: 0.3", "signals.seller_spread.on", "not an attribute"},
		{"against unknown user attribute", `against: "$user.city"`, `against: "$user.town"`, "signals.proximity.against", "not an attribute of user"},
		{"knob label", `label: "My taste  <->  What similar buyers like"`, `label: ""`, "knobs[0].label", "required"},
		{"knob range", "id: horizon\n    label: \"This week  <->  Long-term interests\"\n    range: [0, 1]", "id: horizon\n    label: \"This week  <->  Long-term interests\"\n    range: [1, 1]", "knobs[1].range", "min < max"},
		{"knob target", "maps: { content: \"1 - x\", collaborative: \"x\" }", "maps: { contnt: \"1 - x\", collaborative: \"x\" }", "knobs[0].maps.contnt", "neither a declared signal"},
		{"knob expression", `collaborative: "x" }`, `collaborative: "x +" }`, "knobs[0].maps.collaborative", "expression"},
		{"knob variable", `collaborative: "x" }`, `collaborative: "y" }`, "knobs[0].maps.collaborative", "only use x"},
		{"preset sets a signal", "local_only:   { distance: 0.0 }", "local_only:   { proximity: 0.0 }", "presets.local_only.proximity", "not a declared knob"},
		{"preset out of range", "local_only:   { distance: 0.0 }", "local_only:   { distance: 2.0 }", "presets.local_only.distance", "outside the knob range"},
		{"constraint both", "- require: { attribute: status, equals: active }", "- require: { attribute: status, equals: active }\n    exclude: { interacted: [hide] }", "constraints[0]", "not both"},
		{"constraint attribute", "attribute: status, equals: active", "attribute: statuss, equals: active", "constraints[0].require.attribute", "not an attribute"},
		{"constraint without operator", "attribute: status, equals: active", "attribute: status", "constraints[0].require", "exactly one of"},
		{"constraint interaction", "interacted: [report]", "interacted: [reprot]", "constraints[2].exclude.interacted", "not a declared interaction"},
		{"constraint user ref", `in: "$user.blocked_sellers"`, `in: "$user.blocked"`, "constraints[1].exclude.in", "not an attribute of user"},
		{"recommendable", "recommendable: listing", "recommendable: gadget", "recommendable", "not a declared entity"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(base, c.old) {
				t.Fatalf("test setup: %q not found in the example", c.old)
			}
			text := strings.Replace(base, c.old, c.new, 1)
			s, err := Parse([]byte(text))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			for _, is := range Validate(s) {
				if is.Path == c.path && strings.Contains(is.Message, c.contains) {
					return
				}
			}
			t.Errorf("no issue at %q containing %q; got %v", c.path, c.contains, Validate(s))
		})
	}
}

// Each case edits the trending signal of feed.yml so one rule is broken.
func TestValidateTrendSignal(t *testing.T) {
	base := exampleText(t, "feed.yml")
	cases := []struct {
		name, old, new, path, contains string
	}{
		{"missing window", "window: 6h, ", "", "signals.trending.window", "needs window"},
		{"bad window", "window: 6h", "window: soon", "signals.trending.window", "duration"},
		{"missing baseline", "baseline: 7d, ", "", "signals.trending.baseline", "needs baseline"},
		{"bad baseline", "baseline: 7d", "baseline: never", "signals.trending.baseline", "duration"},
		{"baseline not longer", "baseline: 7d", "baseline: 3h", "signals.trending.baseline", "longer than window"},
		{"auto needs the item's age", "on: created_at, window: 6h", "window: 6h", "signals.trending.on", "needs on"},
		{"age attribute must be a timestamp", "on: created_at, window: 6h", "on: topic, window: 6h", "signals.trending.on", "timestamp attribute"},
		{"age attribute must exist", "on: created_at, window: 6h", "on: made_at, window: 6h", "signals.trending.on", "not an attribute"},
		{"bad against", "against: auto", "against: sometimes", "signals.trending.against", "own, same_age or auto"},
		{"bad count", "count: people", "count: users", "signals.trending.count", "people or events"},
		{"fractional min", "min: 3,", "min: 2.5,", "signals.trending.min", "whole number"},
		{"negative min", "min: 3,", "min: -1,", "signals.trending.min", "whole number"},
		{"ratio of one", "ratio: 2", "ratio: 1", "signals.trending.ratio", "above 1"},
		{"ratio not a number", "ratio: 2", "ratio: high", "signals.trending.ratio", "above 1"},
		{"undeclared interaction", "of: [like, comment, view]", "of: [like, comment, glance]", "signals.trending.of[2]", "not a declared interaction"},
		{"negative interaction", "of: [like, comment, view]", "of: [like, dislike]", "signals.trending.of[1]", "negative"},
		{"empty list", "of: [like, comment, view]", "of: []", "signals.trending.of", "list of interaction names"},
		{"misspelt key", "ratio: 2 }", "ratio: 2, rato: 3 }", "signals.trending.rato", "unknown key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(base, c.old) {
				t.Fatalf("test setup: %q not found in the example", c.old)
			}
			issues := issuesFor(t, strings.Replace(base, c.old, c.new, 1))
			for _, is := range issues {
				if is.Path == c.path && strings.Contains(is.Message, c.contains) {
					return
				}
			}
			t.Errorf("no issue at %q containing %q; got %v", c.path, c.contains, issues)
		})
	}

	// The other legitimate shapes are accepted: own history alone needs no item age, and
	// same_age needs no baseline.
	ok := []struct{ name, old, new string }{
		{"against own, no age attribute", "on: created_at, window: 6h, baseline: 7d, against: auto", "window: 6h, baseline: 7d, against: own"},
		{"against same_age, no baseline", "baseline: 7d, against: auto", "against: same_age"},
		{"defaults for the optional keys", ", of: [like, comment, view], count: people, min: 3, ratio: 2", ""},
		{"events", "count: people", "count: events"},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(base, c.old) {
				t.Fatalf("test setup: %q not found in the example", c.old)
			}
			if issues := issuesFor(t, strings.Replace(base, c.old, c.new, 1)); len(issues) != 0 {
				t.Errorf("want no issues, got %v", issues)
			}
		})
	}
}

func TestValidateComputedAttributes(t *testing.T) {
	base := exampleText(t, "news.yml")
	text := strings.Replace(base, `computed: "len(title)"`, `computed: "len(headline)"`, 1)
	issues := issuesFor(t, text)
	found := false
	for _, is := range issues {
		if is.Path == "entities.article" && strings.Contains(is.Message, "headline") {
			found = true
		}
	}
	if !found {
		t.Errorf("computed reference to a missing attribute not reported: %v", issues)
	}
}

func TestRankedDefaultsToTheOnlyNonUserEntity(t *testing.T) {
	s, err := Parse([]byte(strings.Replace(exampleText(t, "marketplace.yml"), "recommendable: listing", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	// marketplace has seller and listing besides user, so it must be named explicitly
	if _, ok := s.Ranked(); ok {
		t.Error("two non-user entities: Ranked should not guess")
	}
	found := false
	for _, is := range Validate(s) {
		if is.Path == "recommendable" {
			found = true
		}
	}
	if !found {
		t.Error("missing recommendable should be reported when it is ambiguous")
	}

	feed, _ := Parse([]byte(strings.Replace(exampleText(t, "feed.yml"), "recommendable: post", "", 1)))
	if r, ok := feed.Ranked(); !ok || r != "post" {
		t.Errorf("feed Ranked = %q, %v; want post", r, ok)
	}
}

func TestValidateReportsAllIssuesAtOnce(t *testing.T) {
	s, _ := Parse([]byte("version: 0\nentities: {}\ninteractions: {}\nsignals: {}\n"))
	if n := len(Validate(s)); n < 4 {
		t.Errorf("expected several issues at once, got %d", n)
	}
}
