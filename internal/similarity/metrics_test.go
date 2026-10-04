package similarity

import (
	"math"
	"testing"

	"github.com/timurcravtov/walrus/internal/domain"
)

func pair(a, b map[string]domain.Value, attrs ...string) Pair {
	return Pair{A: a, B: b, Attrs: attrs, Range: func(string) (float64, float64) { return 0, 1 }}
}

func TestMetrics(t *testing.T) {
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }
	rock := map[string]domain.Value{"genres": domain.Set("rock", "indie"), "artist": domain.Str("x"), "e": domain.Num(0.9), "v": domain.Num(0.1), "price": domain.Num(10), "emb": domain.Vec(1, 0)}
	pop := map[string]domain.Value{"genres": domain.Set("rock", "pop"), "artist": domain.Str("y"), "e": domain.Num(0.6), "v": domain.Num(0.1), "price": domain.Num(100), "emb": domain.Vec(0, 1)}
	none := map[string]domain.Value{}

	for _, c := range []struct {
		metric string
		a, b   map[string]domain.Value
		attrs  []string
		want   float64
		ok     bool
	}{
		{"jaccard", rock, pop, []string{"genres"}, 1.0 / 3, true},
		{"jaccard", rock, none, []string{"genres"}, 0, false},
		{"equals", rock, rock, []string{"artist"}, 1, true},
		{"equals", rock, pop, []string{"artist"}, 0, true},
		{"closeness", rock, pop, []string{"e"}, 0.7, true},
		{"closeness", rock, none, []string{"e"}, 0, false},
		{"log_ratio", rock, pop, []string{"price"}, 0, true},
		{"cosine", rock, pop, []string{"emb"}, 0, true},
		{"cosine", rock, rock, []string{"e", "v"}, 1, true},
		{"cosine", rock, none, []string{"e", "v"}, 0, false},
	} {
		m, ok := Lookup(c.metric)
		if !ok {
			t.Fatalf("%s is not registered", c.metric)
		}
		got, gotOK := m(pair(c.a, c.b, c.attrs...))
		if gotOK != c.ok || !near(got, c.want) {
			t.Errorf("%s(%v) = %v %v, want %v %v", c.metric, c.attrs, got, gotOK, c.want, c.ok)
		}
	}
}

func TestClosenessOfAnAttributeThatNeverVaries(t *testing.T) {
	m, _ := Lookup("closeness")
	p := Pair{A: map[string]domain.Value{"x": domain.Num(3)}, B: map[string]domain.Value{"x": domain.Num(3)}, Attrs: []string{"x"},
		Range: func(string) (float64, float64) { return 3, 3 }}
	if got, ok := m(p); !ok || got != 1 {
		t.Errorf("got %v %v", got, ok)
	}
}

func TestRegisterAddsAMetric(t *testing.T) {
	Register("always_half", func(Pair) (float64, bool) { return 0.5, true })
	defer func() { mu.Lock(); delete(metrics, "always_half"); mu.Unlock() }()
	m, ok := Lookup("always_half")
	if !ok {
		t.Fatal("not registered")
	}
	if got, _ := m(Pair{}); got != 0.5 {
		t.Errorf("got %v", got)
	}
}
