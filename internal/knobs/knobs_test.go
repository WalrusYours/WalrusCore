package knobs

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/timurcravtov/walrus/internal/schema"
)

func compile(tb testing.TB, name string) *schema.Compiled {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "schema", "schema-examples", name))
	if err != nil {
		tb.Fatal(err)
	}
	s, err := schema.Parse(data)
	if err != nil {
		tb.Fatal(err)
	}
	if issues := schema.Validate(s); len(issues) != 0 {
		tb.Fatalf("%s invalid: %v", name, issues)
	}
	c, err := schema.Compile(s, 1, schema.Hash(s))
	if err != nil {
		tb.Fatal(err)
	}
	return c
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func weight(t *testing.T, c *schema.Compiled, r Resolved, signal string) float64 {
	t.Helper()
	i, ok := c.SignalIndex[signal]
	if !ok {
		t.Fatalf("no signal %q", signal)
	}
	return r.Weights[i]
}

func meta(t *testing.T, c *schema.Compiled, r Resolved, name string) float64 {
	t.Helper()
	i, ok := c.MetaIndex[name]
	if !ok {
		t.Fatalf("no meta %q", name)
	}
	return r.Meta[i]
}

func TestDefaultPresetDrivesTheWeights(t *testing.T) {
	// feed.yml default preset: taste_vs_crowd 0.5, horizon 0.5, explore 0.2
	c := compile(t, "feed.yml")
	r, err := Resolve(c, Input{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"content": 0.5, "collaborative": 0.5, "exploration": 0.2, "author_spread": 0.4,
		"popularity": 0.1, "recency": 0.3, // not bound to any knob: schema defaults
	}
	for sig, w := range want {
		if got := weight(t, c, r, sig); !near(got, w) {
			t.Errorf("%s = %v, want %v", sig, got, w)
		}
	}
	if got := meta(t, c, r, "interactions.half_life_scale"); !near(got, 0.2+(4-0.2)*0.5) {
		t.Errorf("half_life_scale = %v, want 2.1", got)
	}
}

func TestPrecedence(t *testing.T) {
	c := compile(t, "feed.yml")
	explore := func(r Resolved) float64 { return r.Knobs[c.KnobIndex["explore"]] }

	r, _ := Resolve(c, Input{})
	if !near(explore(r), 0.2) {
		t.Errorf("default preset: explore = %v, want 0.2", explore(r))
	}
	r, _ = Resolve(c, Input{Preset: "discover"})
	if !near(explore(r), 0.9) {
		t.Errorf("named preset: explore = %v, want 0.9", explore(r))
	}
	// a preset that sets only some knobs leaves the others on the default preset
	if got := r.Knobs[c.KnobIndex["horizon"]]; !near(got, 0.5) {
		t.Errorf("discover leaves horizon on the default preset, got %v", got)
	}
	r, _ = Resolve(c, Input{Preset: "discover", Saved: map[string]float64{"explore": 0.4}})
	if !near(explore(r), 0.4) {
		t.Errorf("saved beats preset: explore = %v, want 0.4", explore(r))
	}
	r, _ = Resolve(c, Input{Preset: "discover", Saved: map[string]float64{"explore": 0.4}, Overrides: map[string]float64{"explore": 0.1}})
	if !near(explore(r), 0.1) {
		t.Errorf("override beats saved: explore = %v, want 0.1", explore(r))
	}
	if got := weight(t, c, r, "exploration"); !near(got, 0.1) {
		t.Errorf("exploration weight follows the knob: %v", got)
	}
}

func TestWithoutADefaultPresetKnobsStartInTheMiddle(t *testing.T) {
	c := compile(t, "news.yml")
	delete(c.Presets, "default")
	c.DefaultPreset = nil
	r, err := Resolve(c, Input{})
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range c.Knobs {
		if !near(r.Knobs[i], (k.Min+k.Max)/2) {
			t.Errorf("%s = %v, want the middle", k.ID, r.Knobs[i])
		}
	}
}

func TestClampingIsReported(t *testing.T) {
	c := compile(t, "feed.yml")
	r, err := Resolve(c, Input{Overrides: map[string]float64{"explore": 7, "horizon": -3}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Clamped, []string{"explore", "horizon"}) {
		t.Errorf("Clamped = %v", r.Clamped)
	}
	if got := r.Knobs[c.KnobIndex["explore"]]; got != 1 {
		t.Errorf("explore clamped to %v, want 1", got)
	}
	if got := weight(t, c, r, "exploration"); got != 1 {
		t.Errorf("exploration = %v, want 1 (knob clamped to its maximum)", got)
	}
}

func TestErrors(t *testing.T) {
	c := compile(t, "feed.yml")
	if _, err := Resolve(c, Input{Preset: "nope"}); !errors.Is(err, ErrUnknownPreset) {
		t.Errorf("unknown preset: %v", err)
	}
	if _, err := Resolve(c, Input{Overrides: map[string]float64{"nope": 1}}); !errors.Is(err, ErrUnknownKnob) {
		t.Errorf("unknown knob: %v", err)
	}
	if _, err := Resolve(c, Input{Saved: map[string]float64{"nope": 1}}); !errors.Is(err, ErrUnknownKnob) {
		t.Errorf("unknown saved knob: %v", err)
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1)} {
		if _, err := Resolve(c, Input{Overrides: map[string]float64{"explore": bad}}); !errors.Is(err, ErrBadValue) {
			t.Errorf("value %v: %v", bad, err)
		}
	}
	// several problems are reported together
	_, err := Resolve(c, Input{Overrides: map[string]float64{"a": 1, "b": 2}})
	if err == nil || !errors.Is(err, ErrUnknownKnob) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveNeverChangesTheCompiledSchema(t *testing.T) {
	c := compile(t, "marketplace.yml")
	before := slices.Clone(c.Defaults)
	metaBefore := slices.Clone(c.MetaDefaults)
	for i := 0; i < 3; i++ {
		r, err := Resolve(c, Input{Preset: "bargain_hunt", Overrides: map[string]float64{"explore": float64(i) / 2}})
		if err != nil {
			t.Fatal(err)
		}
		r.Weights[0] = 999 // a caller scribbling on its result must not reach the schema
		r.Meta[0] = 999
	}
	if !slices.Equal(before, c.Defaults) || !slices.Equal(metaBefore, c.MetaDefaults) {
		t.Fatal("Resolve modified the compiled schema")
	}
}

func TestEveryPresetOfEveryExampleResolves(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "docs", "schema", "schema-examples", "*.yml"))
	for _, f := range files {
		name := filepath.Base(f)
		c := compile(t, name)
		presets := []string{""}
		for p := range c.Presets {
			presets = append(presets, p)
		}
		for _, p := range presets {
			r, err := Resolve(c, Input{Preset: p})
			if err != nil {
				t.Errorf("%s preset %q: %v", name, p, err)
				continue
			}
			for i, w := range r.Weights {
				if math.IsNaN(w) || math.IsInf(w, 0) {
					t.Errorf("%s preset %q: signal %s = %v", name, p, c.Signals[i].ID, w)
				}
			}
			if len(r.Clamped) != 0 {
				t.Errorf("%s preset %q clamps %v: presets must stay inside the knob ranges", name, p, r.Clamped)
			}
		}
	}
}

func TestResolveIntoReusesBuffersAndAllocatesNothing(t *testing.T) {
	c := compile(t, "marketplace.yml")
	var r Resolved
	in := Input{Preset: "bargain_hunt"}
	if err := ResolveInto(c, in, &r); err != nil { // first call sizes the buffers
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(1000, func() { _ = ResolveInto(c, in, &r) })
	if allocs != 0 {
		t.Errorf("allocations per ResolveInto = %v, want 0", allocs)
	}
}

func BenchmarkResolve(b *testing.B) {
	for _, name := range []string{"feed.yml", "marketplace.yml", "spotify.yml"} {
		c := compile(b, name)
		in := Input{Overrides: map[string]float64{c.Knobs[0].ID: 0.7}}
		b.Run(name+"/Resolve", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = Resolve(c, in)
			}
		})
		b.Run(name+"/ResolveInto", func(b *testing.B) {
			var r Resolved
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = ResolveInto(c, in, &r)
			}
		})
	}
}

// Schema v2: a knob starts at its declared default, not the middle of its range, and the new
// targets (recommender values, cross-type history, signals.<s>.weight) resolve like any other.
func TestKnobDefaultsAndV2Targets(t *testing.T) {
	c := compile(t, "spotify.yml")
	r, err := Resolve(c, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if got := meta(t, c, r, "recommenders.playlist_add.blend_user"); !near(got, 0.15) {
		t.Errorf("blend_user = %v, want 0.15 (toggle defaults to on)", got)
	}
	if got := meta(t, c, r, "recommenders.playlist_add.seed_aggregate"); !near(got, 0) {
		t.Errorf("seed_aggregate = %v, want 0 (fit the whole playlist)", got)
	}
	if got := weight(t, c, r, "sounds_like"); !near(got, 0.35*(1-0.3)) {
		t.Errorf("sounds_like = %v, want %v (vibe_vs_branch_out defaults to 0.3)", got, 0.35*0.7)
	}

	// a saved value still beats the declared default
	r, _ = Resolve(c, Input{Saved: map[string]float64{"mix_in_my_taste": 0}})
	if got := meta(t, c, r, "recommenders.playlist_add.blend_user"); got != 0 {
		t.Errorf("saved toggle off: blend_user = %v, want 0", got)
	}

	shelf := compile(t, "shelf.yml")
	r, _ = Resolve(shelf, Input{})
	if got := meta(t, shelf, r, "signals.films_like_yours.from.book"); !near(got, 1) {
		t.Errorf("from.book = %v, want 1", got)
	}
	if got := meta(t, shelf, r, "recommenders.home.mix.film"); !near(got, 0.5) {
		t.Errorf("mix.film = %v, want 0.5", got)
	}
	if got := meta(t, shelf, r, "attribute.book.pages.target"); !near(got, 500) {
		t.Errorf("pages target = %v, want 500 (lerp(100, 900, 0.5))", got)
	}

	// a choice knob resolves through its option value
	shop := compile(t, "shop.yml")
	r, _ = Resolve(shop, Input{Overrides: map[string]float64{"trend_strength": 1}})
	if got := weight(t, shop, r, "trending"); !near(got, 0.4) {
		t.Errorf("trending = %v, want 0.4", got)
	}
}

// A recommender lists the knobs it offers; the others must not move its weights.
func TestScopeSkipsKnobsOutsideIt(t *testing.T) {
	c := compile(t, "feed.yml")
	all, _ := Resolve(c, Input{Overrides: map[string]float64{"explore": 1}})
	if got := weight(t, c, all, "exploration"); !near(got, 1) {
		t.Fatalf("without a scope exploration = %v, want 1", got)
	}
	// explore is outside the scope: its binding is skipped, exploration keeps the schema default
	scoped, err := Resolve(c, Input{Overrides: map[string]float64{"explore": 1}, Scope: []string{"taste_vs_crowd"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := weight(t, c, scoped, "exploration"); !near(got, 0.1) {
		t.Errorf("out of scope exploration = %v, want the default 0.1", got)
	}
	if got := scoped.Knobs[c.KnobIndex["explore"]]; got != 1 {
		t.Errorf("the knob's own value must still resolve, got %v", got)
	}
	if got := weight(t, c, scoped, "content"); !near(got, 0.5) {
		t.Errorf("in-scope knob still applies: content = %v, want 0.5", got)
	}
	// an empty, non-nil scope means no knob applies at all
	none, _ := Resolve(c, Input{Scope: []string{}})
	if got := weight(t, c, none, "collaborative"); !near(got, 0.5) {
		t.Errorf("empty scope: collaborative = %v, want the default 0.5", got)
	}
}
