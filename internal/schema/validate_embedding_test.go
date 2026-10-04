package schema

import (
	"slices"
	"strings"
	"testing"
)

const (
	// the signal as movies.yml ships it, and the same signal with every setting spelled out, so the
	// rules below have each key to break
	shippedSignal = "{ type: embedding, for: film, factors: 32, of: [watch, rate, watchlist], default: 0.25 }"
	fullSignal    = "{ type: embedding, for: film, factors: 32, regularization: 0.1, alpha: 10, iterations: 15, of: [watch, rate, watchlist], default: 0.25 }"
)

// withEmbedding is movies.yml, which ships a learned-taste signal for films that the home and
// similar_films recommenders use as a score and as a candidate source, with every setting of the
// signal spelled out.
func withEmbedding(t *testing.T) string {
	t.Helper()
	text := exampleText(t, "movies.yml")
	if !strings.Contains(text, shippedSignal) {
		t.Fatal("test setup: movies.yml no longer declares hidden_taste the way these tests expect")
	}
	return strings.Replace(text, shippedSignal, fullSignal, 1)
}

func TestTheShippedMoviesExampleUsesALearnedTaste(t *testing.T) {
	text := exampleText(t, "movies.yml")
	if issues := issuesFor(t, text); len(issues) != 0 {
		t.Fatalf("movies.yml must be valid: %v", issues)
	}
	s, err := Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	sg := s.Signals["hidden_taste"]
	if sg.Type != "embedding" || sg.Params["factors"] != 32 || len(sg.For) != 1 || sg.For[0] != "film" {
		t.Errorf("hidden_taste = %+v", sg)
	}
	for _, name := range []string{"home", "similar_films"} {
		r := s.Recommenders[name]
		var usesSource, usesSignal bool
		for _, c := range r.Candidates {
			usesSource = usesSource || c.Source == "factors" && c.Signal == "hidden_taste"
		}
		for _, id := range r.Signals {
			usesSignal = usesSignal || id == "hidden_taste"
		}
		if !usesSource || !usesSignal {
			t.Errorf("%s should use the learned taste as a candidate source (%v) and a score (%v)", name, usesSource, usesSignal)
		}
	}
	if r := s.Recommenders["top_films"]; slices.Contains(r.Signals, "hidden_taste") {
		t.Error("top_films is not personal, so it has no use for a learned taste")
	}
	// a user can move it: the taste slider reaches the signal
	knob, _ := s.Knob("taste_vs_crowd")
	if _, ok := knob.Maps["hidden_taste"]; !ok {
		t.Errorf("the taste slider should weigh the learned taste: %v", knob.Maps)
	}
}

func TestEmbeddingSignalValidates(t *testing.T) {
	base := withEmbedding(t)
	if issues := issuesFor(t, base); len(issues) != 0 {
		t.Fatalf("a well-formed embedding signal and factors source must be valid: %v", issues)
	}
	s, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	sg := s.Signals["hidden_taste"]
	if sg.Type != "embedding" || sg.Params["factors"] != 32 || sg.Params["regularization"] != 0.1 || sg.Params["alpha"] != 10 {
		t.Errorf("parsed signal = %+v", sg)
	}

	// the settings are all optional: the engine's defaults apply
	bare := strings.Replace(base, fullSignal, "{ type: embedding, default: 0.25 }", 1)
	if issues := issuesFor(t, bare); len(issues) != 0 {
		t.Errorf("an embedding with no settings must be valid when the schema ranks one entity type: %v", issues)
	}
}

func TestEmbeddingRules(t *testing.T) {
	signal := fullSignal
	runCases := func(cases []v2Case) {
		base := withEmbedding(t)
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				if !strings.Contains(base, c.old) {
					t.Fatalf("test setup: %q not found", c.old)
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
	}
	edit := func(from, to string) (string, string) { return signal, strings.Replace(signal, from, to, 1) }

	var cases []v2Case
	for _, c := range []struct{ name, from, to, path, contains string }{
		{"no factors", "factors: 32", "factors: 0", "signals.hidden_taste.factors", "whole number from 1 to 256"},
		{"too many factors", "factors: 32", "factors: 257", "signals.hidden_taste.factors", "whole number from 1 to 256"},
		{"fractional factors", "factors: 32", "factors: 2.5", "signals.hidden_taste.factors", "whole number"},
		{"text for factors", "factors: 32", "factors: many", "signals.hidden_taste.factors", "whole number"},
		{"zero regularization", "regularization: 0.1", "regularization: 0", "signals.hidden_taste.regularization", "above 0"},
		{"negative alpha", "alpha: 10", "alpha: -1", "signals.hidden_taste.alpha", "above 0"},
		{"no iterations", "iterations: 15", "iterations: 0", "signals.hidden_taste.iterations", "whole number from 1 to 200"},
		{"negative interaction trains it", "of: [watch, rate, watchlist]", "of: [watch, abandon]", "signals.hidden_taste.of[1]", "only positive"},
		{"unknown interaction", "of: [watch, rate, watchlist]", "of: [watch, nope]", "signals.hidden_taste.of[1]", "nope"},
		{"unknown key", "factors: 32", "factors: 32, rank: 3", "signals.hidden_taste.rank", "unknown key"},
		{"history from another entity", "for: film,", "for: film, from: [director],", "signals.hidden_taste.from", "no interaction targets director"},
	} {
		old, repl := edit(c.from, c.to)
		cases = append(cases, v2Case{c.name, old, repl, c.path, c.contains})
	}
	cases = append(cases,
		v2Case{"factors source needs an embedding signal", "{ source: factors, signal: hidden_taste, cap: 100 }", "{ source: factors, signal: popularity, cap: 100 }",
			"recommenders.home.candidates[4].signal", "needs signal: <a embedding signal>"},
		v2Case{"factors source without a signal", "{ source: factors, signal: hidden_taste, cap: 100 }", "{ source: factors, cap: 100 }",
			"recommenders.home.candidates[4].signal", "needs signal"},
		v2Case{"factors source with nothing to fit", "label: Top films\n    for: [film]\n    seed: none\n",
			"label: Top films\n    for: [film]\n    seed: none\n    candidates:\n      - { source: factors, signal: hidden_taste }\n",
			"recommenders.top_films.candidates[0].source", "needs something to fit"},
	)
	runCases(cases)
}

func TestEmbeddingLearnsOneEntityType(t *testing.T) {
	// a schema that ranks two entity types cannot leave `for` out: the model has one row per item
	text := exampleText(t, "shelf.yml") // recommendable: [book, film]
	if !strings.Contains(text, "\nsignals:\n") {
		t.Fatal("test setup: shelf.yml has no signals section")
	}
	for name, signal := range map[string]string{
		"without for":    "  hidden_taste: { type: embedding, default: 0.2 }\n",
		"for both types": "  hidden_taste: { type: embedding, for: [book, film], default: 0.2 }\n",
	} {
		t.Run(name, func(t *testing.T) {
			edited := strings.Replace(text, "\nsignals:\n", "\nsignals:\n"+signal, 1)
			for _, is := range issuesFor(t, edited) {
				if is.Path == "signals.hidden_taste.for" && strings.Contains(is.Message, "one entity type") {
					return
				}
			}
			t.Error("an embedding over several entity types was accepted")
		})
	}
	// naming one type is enough
	one := strings.Replace(text, "\nsignals:\n", "\nsignals:\n  hidden_taste: { type: embedding, for: book, default: 0.2 }\n", 1)
	for _, is := range issuesFor(t, one) {
		if strings.HasPrefix(is.Path, "signals.hidden_taste") {
			t.Errorf("for: book should be valid, got %v", is)
		}
	}
}

func TestTierOfIsTypeAware(t *testing.T) {
	s := &Schema{Signals: map[string]SignalSpec{
		"taste":   {Type: "embedding"},
		"content": {Type: "item_neighbors"},
	}}
	for path, want := range map[string]Tier{
		"signals.taste.from":            TierT1, // picks the training data
		"signals.taste.factors":         TierT1,
		"signals.taste.regularization":  TierT1,
		"signals.taste.alpha":           TierT1,
		"signals.taste.iterations":      TierT1,
		"signals.taste.of":              TierT1,
		"signals.taste.default":         TierT3, // a knob may move it
		"signals.taste.explain":         TierT3,
		"signals.taste.normalise":       TierT3,
		"signals.content.from":          TierT3, // another type: it only weights history
		"signals.content.from.film":     TierT3,
		"signals.missing.from":          TierT3,
		"recommenders.home.candidates":  TierT2,
		"similarity.film":               TierT1,
		"entities.film.attributes.year": TierT0,
	} {
		if got := s.TierOf(path); got != want {
			t.Errorf("TierOf(%q) = %s, want %s", path, got, want)
		}
	}
}
