package schema

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/similarity"
)

func load(t *testing.T, name string) *Schema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "schema", "schema-examples", name))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return s
}

// The same Go types must hold two unrelated schemas: that is the content-agnostic claim.
func TestParseExampleSchemas(t *testing.T) {
	for _, tc := range []struct {
		file          string
		recommendable string
		entities      int
		signals       int
		knobs         int
	}{
		{"marketplace.yml", "listing", 3, 7, 4},
		{"feed.yml", "post", 2, 7, 3},
	} {
		s := load(t, tc.file)
		if len(s.Recommendable) != 1 || s.Recommendable[0] != tc.recommendable {
			t.Errorf("%s: recommendable = %q, want %q", tc.file, s.Recommendable, tc.recommendable)
		}
		if len(s.Entities) != tc.entities || len(s.Signals) != tc.signals || len(s.Knobs) != tc.knobs {
			t.Errorf("%s: entities/signals/knobs = %d/%d/%d, want %d/%d/%d", tc.file,
				len(s.Entities), len(s.Signals), len(s.Knobs), tc.entities, tc.signals, tc.knobs)
		}
	}
}

func TestMarketplaceDetails(t *testing.T) {
	s := load(t, "marketplace.yml")

	emb := s.Entities["listing"].Attributes["text_embedding"]
	if emb.Type != TypeVector || emb.Dim != 384 || !emb.Optional {
		t.Errorf("text_embedding = %+v", emb)
	}
	if r := s.Entities["seller"].Attributes["rating"].Range; r == nil || r[0] != 0 || r[1] != 5 {
		t.Errorf("rating range = %v", r)
	}

	view := s.Interactions["view"]
	if view.Value != "seconds" || view.Transform != TransformLog1p || view.HalfLife.Std() != 72*time.Hour {
		t.Errorf("view = %+v", view)
	}
	if s.Interactions["hide"].Weight != -5 {
		t.Errorf("hide weight = %v", s.Interactions["hide"].Weight)
	}

	// Signal-specific keys land in Params, so new signal types need no struct change.
	rec := s.Signals["recency"]
	if rec.Type != "age_decay" || rec.Default != 0.3 || rec.Params["on"] != "created_at" || rec.Params["half_life"] != "5d" {
		t.Errorf("recency = %+v", rec)
	}
	if s.Signals["proximity"].Params["against"] != "$user.city" {
		t.Errorf("proximity params = %v", s.Signals["proximity"].Params)
	}

	// A knob maps to a signal or to a meta-parameter whose key contains a dot.
	horizon, ok := s.Knob("horizon")
	if !ok || horizon.Maps["interactions.half_life_scale"] != "lerp(0.2, 4, x)" {
		t.Errorf("horizon = %+v", horizon)
	}
	if _, ok := s.Knob("nope"); ok {
		t.Error("Knob should report missing ids")
	}

	if s.Presets["bargain_hunt"]["explore"] != 0.8 {
		t.Errorf("preset = %v", s.Presets["bargain_hunt"])
	}

	// Constraints: each is exactly one of require/exclude.
	if len(s.Constraints) != 5 {
		t.Fatalf("constraints = %d, want 5", len(s.Constraints))
	}
	if c := s.Constraints[0]; c.Require == nil || c.Exclude != nil || c.Require.Equals != "active" {
		t.Errorf("constraint 0 = %+v", c)
	}
	if c := s.Constraints[2]; c.Exclude == nil || len(c.Exclude.Interacted) != 1 || c.Exclude.Interacted[0] != "report" {
		t.Errorf("constraint 2 = %+v", c)
	}
	if c := s.Constraints[4]; c.Exclude == nil || c.Exclude.Gt != "$user.max_budget * 1.2" || c.Exclude.When == "" {
		t.Errorf("constraint 4 = %+v", c)
	}

	if terms := s.Similarity["user"]; len(terms) != 1 || terms[0].Via != "interactions" || terms[0].Metric != MetricCosine {
		t.Errorf("user similarity = %+v", terms)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	// A typo must fail loudly, not silently drop the rule it was meant to set.
	if _, err := Parse([]byte("version: 1\nentitiez: {}\n")); err == nil {
		t.Error("unknown top-level key should be rejected")
	}
	bad := "version: 1\nentities:\n  post:\n    key: id\n    atributes: {}\n"
	if _, err := Parse([]byte(bad)); err == nil {
		t.Error("unknown key inside an entity should be rejected")
	}
	if _, err := Parse(nil); err == nil {
		t.Error("empty input should be an error")
	}
	if _, err := Parse([]byte("version: [")); err == nil {
		t.Error("invalid YAML should be an error")
	}
}

func TestSortedAccessors(t *testing.T) {
	s := load(t, "feed.yml")
	ids := s.SignalIDs()
	for i := 1; i < len(ids); i++ {
		if ids[i-1] > ids[i] {
			t.Fatalf("SignalIDs not sorted: %v", ids)
		}
	}
	if len(s.EntityTypes()) != 2 || len(s.InteractionTypes()) != 4 {
		t.Errorf("entity/interaction types = %v / %v", s.EntityTypes(), s.InteractionTypes())
	}
}

func TestParseDuration(t *testing.T) {
	ok := map[string]time.Duration{
		"":     0,
		"30s":  30 * time.Second,
		"90m":  90 * time.Minute,
		"12h":  12 * time.Hour,
		"3d":   72 * time.Hour,
		"2w":   14 * 24 * time.Hour,
		"1.5d": 36 * time.Hour,
		"2y":   730 * 24 * time.Hour, // a year is 365 days
	}
	for in, want := range ok {
		got, err := ParseDuration(in)
		if err != nil || got.Std() != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got.Std(), err, want)
		}
	}
	for _, in := range []string{"3", "d", "3 d", "-3d", "3x", "abc", "1..5d"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) should fail", in)
		}
	}
	// years parse but never print, so a v1 schema that says 365d keeps its canonical form
	if got := Duration(365 * 24 * time.Hour).String(); got != "365d" {
		t.Errorf("String() = %q, want 365d", got)
	}
	if got := Duration(72 * time.Hour).String(); got != "3d" {
		t.Errorf("String() = %q, want 3d", got)
	}
	if got := Duration(36 * time.Hour).String(); got != "36h" {
		t.Errorf("String() = %q, want 36h", got)
	}
}

func TestAttrTypeKinds(t *testing.T) {
	want := map[AttrType]domain.Kind{
		TypeCategorical: domain.KindString, TypeString: domain.KindString, TypeRef: domain.KindString,
		TypeFloat: domain.KindFloat, TypeInt: domain.KindFloat, TypeBool: domain.KindBool,
		TypeTimestamp: domain.KindTime, TypeSet: domain.KindSet, TypeVector: domain.KindVector, TypeGeo: domain.KindVector,
	}
	if len(want) != len(AttrTypes) {
		t.Fatalf("test covers %d types, AttrTypes has %d", len(want), len(AttrTypes))
	}
	for typ, k := range want {
		if !typ.Valid() || typ.Kind() != k {
			t.Errorf("%s: valid=%v kind=%v, want %v", typ, typ.Valid(), typ.Kind(), k)
		}
	}
	if AttrType("blob").Valid() || AttrType("blob").Kind() != domain.KindNull {
		t.Error("unknown type should be invalid with null kind")
	}
}

func TestCoerce(t *testing.T) {
	rating := AttributeSpec{Type: TypeFloat, Range: &[2]float64{0, 5}}
	vec3 := AttributeSpec{Type: TypeVector, Dim: 3}
	ts := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	good := []struct {
		name string
		spec AttributeSpec
		in   any
		want domain.Value
	}{
		{"categorical", AttributeSpec{Type: TypeCategorical}, "bikes", domain.Str("bikes")},
		{"ref", AttributeSpec{Type: TypeRef, Entity: "seller"}, "s_77", domain.Str("s_77")},
		{"float", AttributeSpec{Type: TypeFloat}, 180.5, domain.Num(180.5)},
		{"int from json number", AttributeSpec{Type: TypeInt}, float64(7), domain.Num(7)},
		{"range inclusive", rating, 5.0, domain.Num(5)},
		{"bool", AttributeSpec{Type: TypeBool}, true, domain.Bool(true)},
		{"timestamp string", AttributeSpec{Type: TypeTimestamp}, "2026-09-01T10:00:00Z", domain.Time(ts)},
		{"timestamp time.Time", AttributeSpec{Type: TypeTimestamp}, ts, domain.Time(ts)},
		{"set from json", AttributeSpec{Type: TypeSet}, []any{"road", "shimano", "road"}, domain.Set("road", "shimano")},
		{"vector", vec3, []any{1.0, 2.0, 3.0}, domain.Vec(1, 2, 3)},
		{"geo", AttributeSpec{Type: TypeGeo}, map[string]any{"lat": 47.5, "lon": 28.25}, domain.Vec(47.5, 28.25)},
		{"optional nil", AttributeSpec{Type: TypeFloat, Optional: true}, nil, domain.Null()},
	}
	for _, c := range good {
		got, err := c.spec.Coerce(c.in)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("%s: Coerce(%v) = %v, %v; want %v", c.name, c.in, got, err, c.want)
		}
	}

	bad := []struct {
		name string
		spec AttributeSpec
		in   any
	}{
		{"string given number", AttributeSpec{Type: TypeCategorical}, 3.0},
		{"float given string", AttributeSpec{Type: TypeFloat}, "3"},
		{"int with fraction", AttributeSpec{Type: TypeInt}, 2.5},
		{"below range", rating, -0.1},
		{"above range", rating, 5.1},
		{"nan", AttributeSpec{Type: TypeFloat}, nan()},
		{"bool given string", AttributeSpec{Type: TypeBool}, "true"},
		{"bad timestamp", AttributeSpec{Type: TypeTimestamp}, "yesterday"},
		{"set with number", AttributeSpec{Type: TypeSet}, []any{"a", 1.0}},
		{"vector wrong dim", vec3, []any{1.0, 2.0}},
		{"geo as a list", AttributeSpec{Type: TypeGeo}, []any{47.5, 28.25}},
		{"geo out of range", AttributeSpec{Type: TypeGeo}, map[string]any{"lat": 95.0, "lon": 0.0}},
		{"vector bad element", vec3, []any{1.0, "x", 3.0}},
		{"required nil", AttributeSpec{Type: TypeFloat}, nil},
		{"unknown type", AttributeSpec{Type: "blob"}, "x"},
	}
	for _, c := range bad {
		if v, err := c.spec.Coerce(c.in); err == nil {
			t.Errorf("%s: Coerce(%v) = %v, want an error", c.name, c.in, v)
		}
	}
}

func nan() float64 { return math.NaN() }

var knownSignalTypes = []string{
	"item_neighbors", "user_neighbors", "own_history", "global_count",
	"age_decay", "low_exposure", "attribute_match", "diversity_rerank", "trend",
	"co_occurrence", "sequence", "mutual_connections", "attribute_target", "attribute_value",
	"context_match", "provided", "formula", "satiation", "recurrence", "proximity",
}

var metaTargets = []string{"interactions.half_life_scale", "constraint.energy_center"}

// Every shipped example must parse and be internally consistent.
func TestExamplesAreConsistent(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "docs", "schema", "schema-examples", "*.yml"))
	if err != nil || len(files) < 6 {
		t.Fatalf("expected at least 6 examples, found %d (%v)", len(files), err)
	}
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			s := load(t, name)
			fail := func(format string, args ...any) { t.Helper(); t.Errorf(format, args...) }

			for _, r := range s.Recommendable {
				if _, ok := s.Entities[r]; !ok {
					fail("recommendable %q is not a declared entity", r)
				}
			}
			if _, ok := s.Entities["user"]; !ok {
				fail("no user entity")
			}
			for et, e := range s.Entities {
				for an, a := range e.Attributes {
					if !a.Type.Valid() {
						fail("%s.%s: invalid type %q", et, an, a.Type)
					}
					if a.Type == TypeRef {
						if _, ok := s.Entities[a.Entity]; !ok {
							fail("%s.%s: ref to undeclared entity %q", et, an, a.Entity)
						}
					}
				}
			}
			for it, i := range s.Interactions {
				if i.Target != "" {
					if _, ok := s.Entities[i.Target]; !ok {
						fail("interaction %s targets undeclared entity %q", it, i.Target)
					}
				}
			}
			for et, terms := range s.Similarity {
				if et == "cross" { // cross-type terms: checked by Validate
					continue
				}
				e, ok := s.Entities[et]
				if !ok {
					fail("similarity for undeclared entity %q", et)
					continue
				}
				for _, term := range terms {
					if !term.Metric.Valid() {
						fail("similarity %s: invalid metric %q", et, term.Metric)
					}
					for _, on := range term.On {
						if _, ok := e.Attributes[on]; !ok {
							fail("similarity %s: %q is not an attribute", et, on)
						}
					}
				}
			}
			for id, sig := range s.Signals {
				if !slices.Contains(knownSignalTypes, sig.Type) {
					fail("signal %s: unknown type %q", id, sig.Type)
				}
			}
			knobs := map[string]bool{}
			for _, k := range s.Knobs {
				knobs[k.ID] = true
				if r := k.EffectiveRange(); r[0] >= r[1] || k.Label.IsZero() {
					fail("knob %s: bad range or empty label", k.ID)
				}
				for target := range k.Maps {
					_, isSignal := s.Signals[target]
					// v2 targets are dotted paths (similarity.<e>.<term>.weight...), checked by Validate.
					if !isSignal && !slices.Contains(metaTargets, target) && !strings.Contains(target, ".") {
						fail("knob %s maps to %q, which is neither a signal nor a meta-parameter", k.ID, target)
					}
				}
			}
			for pn, p := range s.Presets {
				for kid := range p {
					if !knobs[kid] {
						fail("preset %s sets %q, which is not a knob", pn, kid)
					}
				}
			}
			for i, c := range s.Constraints {
				if (c.Require == nil) == (c.Exclude == nil) {
					fail("constraint %d must have exactly one of require or exclude", i)
				}
			}
		})
	}
}

// Every metric the schema language accepts must have an implementation, and no implementation may
// be unreachable from a schema.
func TestEverySchemaMetricIsImplemented(t *testing.T) {
	names := similarity.Names()
	for _, m := range Metrics {
		if !slices.Contains(names, string(m)) {
			t.Errorf("metric %q is accepted by the schema but not registered in internal/similarity", m)
		}
	}
	if len(names) != len(Metrics) {
		t.Errorf("registered %v, schema accepts %v", names, Metrics)
	}
}
