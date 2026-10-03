package schema

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPathTier(t *testing.T) {
	for path, want := range map[string]Tier{
		"entities.listing.attributes.price":     TierT0,
		"recommendable":                         TierT0,
		"interactions.view.weight":              TierT0,
		"interactions.view.half_life":           TierT3,
		"similarity.listing":                    TierT1,
		"signals.trending.window":               TierT1,
		"signals.trending.default":              TierT3,
		"signals.people_like_you.min_neighbors": TierT3,
		"signals.content.from.book":             TierT3,
		"signals.content.for":                   TierT2,
		"constraints.not_hidden":                TierT2,
		"rules.seller_cap.quota.max":            TierT3,
		"knobs.taste_vs_crowd.default":          TierT3,
		"presets.default.taste_vs_crowd":        TierT3,
		"recommenders.home.weights.trending":    TierT3,
		"recommenders.home.mix.shares.book":     TierT3,
		"recommenders.home.candidates":          TierT2,
		"recommenders.home.seed":                TierT2,
		"feedback.reasons.have_it.effect":       TierT2,
		"recurrence.learn.window":               TierT1,
		"metrics.ctr":                           TierNever,
		"experiments.x.traffic":                 TierNever,
		"privacy.opt_out.use":                   TierNever,
		"nonsense.path":                         TierUnknown,
	} {
		if got := PathTier(path); got != want {
			t.Errorf("PathTier(%q) = %s, want %s", path, got, want)
		}
	}
}

func TestApplyPatch(t *testing.T) {
	s := load(t, "shop.yml")

	patched, err := ApplyPatch(s, map[string]any{
		"knobs.taste_vs_crowd.default":       0.9,                                                                           // a list item by id
		"recommenders.home.weights.trending": 0.4,                                                                           // a map path
		"constraints.cheap":                  map[string]any{"require": map[string]any{"attribute": "price", "lt": "1000"}}, // added
		"rules.fresh_share":                  nil,                                                                           // removed
	})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := patched.Knob("taste_vs_crowd")
	if k.Default == nil || *k.Default != 0.9 {
		t.Errorf("knob default = %v, want 0.9", k.Default)
	}
	if w := patched.Recommenders["home"].Weights["trending"]; w != "0.4" {
		t.Errorf("weight = %q, want 0.4", w)
	}
	found := false
	for _, c := range patched.Constraints {
		if c.ID == "cheap" && c.Require != nil && c.Require.Attribute == "price" {
			found = true
		}
	}
	if !found {
		t.Error("constraint cheap was not added")
	}
	for _, r := range patched.Rules {
		if r.ID == "fresh_share" {
			t.Error("rule fresh_share was not removed")
		}
	}
	if len(patched.Experiments) != 0 || patched.Holdout != nil {
		t.Error("the patched copy must drop experiments and the holdout")
	}
	// the original is untouched
	if k, _ := s.Knob("taste_vs_crowd"); *k.Default != 0.5 {
		t.Errorf("ApplyPatch changed the original: default = %v", *k.Default)
	}

	for name, set := range map[string]map[string]any{
		"unknown id":          {"knobs.nope.default": 1},
		"list without ids":    {"meta.owners.x": "y"},
		"wrong type of value": {"recommenders.home.limit": "lots"},
	} {
		if _, err := ApplyPatch(s, set); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// v2 value types: a one-name list and a plain text marshal exactly as v1 wrote them, so a v1
// schema's canonical form (and its hash) does not change.
func TestV2TypesKeepTheV1CanonicalForm(t *testing.T) {
	b, _ := yaml.Marshal(struct {
		R StringList `yaml:"r"`
		L Text       `yaml:"l"`
	}{StringList{"post"}, Text{Plain: "Hi"}})
	if got := string(b); got != "r: post\nl: Hi\n" {
		t.Errorf("marshal = %q", got)
	}
	v1 := exampleText(t, "feed.yml")
	s1, _ := Parse([]byte(v1))
	s2, _ := Parse([]byte(strings.ReplaceAll(v1, "recommendable: post", "recommendable: [post]")))
	if Hash(s1) != Hash(s2) {
		t.Error("recommendable: post and recommendable: [post] should hash the same")
	}
}

func TestTextIn(t *testing.T) {
	plain := Text{Plain: "Hello"}
	multi := Text{Locales: map[string]string{"en": "Hello", "ru": "Привет"}}
	for _, c := range []struct {
		t        Text
		locale   string
		fallback string
		want     string
	}{
		{plain, "ru", "en", "Hello"},
		{multi, "ru", "en", "Привет"},
		{multi, "ro", "en", "Hello"},
	} {
		if got := c.t.In(c.locale, c.fallback); got != c.want {
			t.Errorf("In(%s) = %q, want %q", c.locale, got, c.want)
		}
	}
}
