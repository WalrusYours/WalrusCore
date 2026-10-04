package learn

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/factors"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store"
	"github.com/timurcravtov/walrus/internal/store/memory"
)

var now = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

const base = `version: 1
meta: { name: Test, locales: [en], default_locale: en }
entities:
  user: { key: id }
  film:
    key: id
    attributes:
      title: { type: string }
interactions:
  watch: { kind: positive, weight: 2, half_life: 60d }
  like:  { kind: positive, weight: 1 }
  skip:  { kind: negative, weight: -1 }
signals:
  taste: { type: embedding, for: film, factors: 4, regularization: 0.1, alpha: 10, iterations: 10, default: 0.5 }
recommendable: film
`

func compile(t *testing.T, text string) *schema.Compiled {
	t.Helper()
	svc := schema.NewService()
	if res := svc.Load([]byte(text), schema.LoadOptions{Author: "test"}); !res.OK {
		t.Fatalf("the test schema is invalid: %v", res.Errors)
	}
	return svc.Compiled()
}

func film(i int) domain.Entity {
	id := fmt.Sprintf("f%02d", i)
	return domain.Entity{Type: "film", ID: domain.EntityID(id), Attrs: map[string]domain.Value{"title": domain.Str(id)}}
}

// twoCrowds fills a store with 12 films and 30 viewers: the first 15 watch films 0-5, the other 15
// watch films 6-11, each four of their six.
func twoCrowds(t *testing.T) store.Store {
	t.Helper()
	st := memory.New()
	ctx := context.Background()
	var entities []domain.Entity
	for i := 0; i < 12; i++ {
		entities = append(entities, film(i))
	}
	if err := st.UpsertEntities(ctx, entities); err != nil {
		t.Fatal(err)
	}
	var xs []domain.Interaction
	rng := rand.New(rand.NewPCG(5, 9))
	for u := 0; u < 30; u++ {
		first := 0
		if u >= 15 {
			first = 6
		}
		for k, pick := range rng.Perm(6)[:4] {
			xs = append(xs, domain.Interaction{
				User: domain.UserID(fmt.Sprintf("u%02d", u)), Type: "watch",
				Target: domain.EntityID(fmt.Sprintf("f%02d", first+pick)), TS: now.Add(-time.Duration(u+k) * time.Hour),
			})
		}
	}
	if err := st.UpsertInteractions(ctx, xs); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestParamsOfReadsTheSchemaAndDefaultsTheRest(t *testing.T) {
	c := compile(t, base)
	p := ParamsOf(c.Schema.Signals["taste"])
	if p.Factors != 4 || p.Regularization != 0.1 || p.Alpha != 10 || p.Iterations != 10 {
		t.Errorf("params = %+v", p)
	}
	bare := compile(t, strings.Replace(base, "factors: 4, regularization: 0.1, alpha: 10, iterations: 10, ", "", 1))
	d := ParamsOf(bare.Schema.Signals["taste"])
	if d != factors.DefaultParams() {
		t.Errorf("a signal with no settings = %+v, want the defaults %+v", d, factors.DefaultParams())
	}
	if d.Factors != 32 {
		t.Errorf("default factors = %d, want 32", d.Factors)
	}
	// an integer alpha and a fractional one both read as numbers
	for _, alpha := range []string{"alpha: 3", "alpha: 2.5"} {
		c := compile(t, strings.Replace(base, "alpha: 10", alpha, 1))
		if got := ParamsOf(c.Schema.Signals["taste"]).Alpha; got != map[string]float64{"alpha: 3": 3, "alpha: 2.5": 2.5}[alpha] {
			t.Errorf("%s read as %v", alpha, got)
		}
	}
}

func TestOfIsTheNamedInteractionsOrEveryPositiveOne(t *testing.T) {
	c := compile(t, base)
	if got := Of(c.Schema, c.Schema.Signals["taste"]); !slices.Equal(got, []string{"like", "watch"}) {
		t.Errorf("default of = %v, want every positive interaction, sorted: [like watch]", got)
	}
	named := compile(t, strings.Replace(base, "iterations: 10,", "iterations: 10, of: [watch],", 1))
	if got := Of(named.Schema, named.Schema.Signals["taste"]); !slices.Equal(got, []string{"watch"}) {
		t.Errorf("named of = %v", got)
	}
}

func TestGatherWeighsAndFadesInteractions(t *testing.T) {
	c := compile(t, base)
	items := []domain.EntityID{"f00", "f01"}
	val := 0.0
	history := []domain.Interaction{
		{User: "b", Type: "watch", Target: "f00", TS: now},                            // weight 2, no fade
		{User: "b", Type: "watch", Target: "f01", TS: now.Add(-60 * 24 * time.Hour)},  // one half-life old: 1
		{User: "a", Type: "like", Target: "f01", TS: now.Add(-1000 * 24 * time.Hour)}, // no half-life: stays 1
		{User: "a", Type: "skip", Target: "f00", TS: now},                             // negative: left out
		{User: "a", Type: "watch", Target: "zzz", TS: now},                            // unknown item: left out
		{User: "a", Type: "nope", Target: "f00", TS: now},                             // unknown type: left out
		{User: "c", Type: "watch", Target: "f00", TS: now.Add(48 * time.Hour)},        // in the future: not boosted
		{User: "d", Type: "watch", Target: "f01", TS: now, Value: &val},               // value on a type that takes none: ignored
	}
	users, obs := gather(items, history, c, []string{"like", "watch"}, now)
	if users != 4 { // a, b, c, d; users numbered in sorted order
		t.Errorf("users = %d, want 4", users)
	}
	want := map[[2]int]float64{{1, 0}: 2, {1, 1}: 1, {0, 1}: 1, {2, 0}: 2, {3, 1}: 2}
	got := map[[2]int]float64{}
	for _, o := range obs {
		got[[2]int{o.User, o.Item}] += o.Value
	}
	if len(got) != len(want) {
		t.Fatalf("observations = %v, want %v", got, want)
	}
	for k, w := range want {
		if math.Abs(got[k]-w) > 1e-9 {
			t.Errorf("observation %v = %v, want %v", k, got[k], w)
		}
	}
}

func TestTrainSavesAVersionedModel(t *testing.T) {
	ctx := context.Background()
	st, c := twoCrowds(t), compile(t, base)

	res, err := Train(ctx, st, c, "taste", now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != 1 || res.Items != 12 || res.Users != 30 || res.Interactions != 120 || res.Error != "" {
		t.Errorf("result = %+v", res)
	}
	m, err := st.Model(ctx, "taste")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 || m.Factors != 4 || len(m.Items) != 12 || !slices.Equal(m.Of, []string{"like", "watch"}) || !m.TrainedAt.Equal(now) {
		t.Errorf("stored model = version %d, factors %d, items %d, of %v, trained %v", m.Version, m.Factors, len(m.Items), m.Of, m.TrainedAt)
	}

	// the model learned the two crowds: on average, films of one crowd are much closer than films
	// of different crowds (rows follow the sorted ids, so rows 0-5 are one crowd and 6-11 the other)
	var within, across float64
	var nWithin, nAcross int
	for a := 0; a < 12; a++ {
		for b := a + 1; b < 12; b++ {
			if (a < 6) == (b < 6) {
				within += m.Cosine(a, b)
				nWithin++
			} else {
				across += m.Cosine(a, b)
				nAcross++
			}
		}
	}
	within, across = within/float64(nWithin), across/float64(nAcross)
	if within < across+0.3 {
		t.Errorf("mean cosine within a crowd %.2f, across crowds %.2f; want within at least 0.3 higher", within, across)
	}

	if res, _ := Train(ctx, st, c, "taste", now); res.Version != 2 {
		t.Errorf("training again gave version %d, want 2", res.Version)
	}
}

func TestTrainRefusesWhatIsNotAnEmbedding(t *testing.T) {
	st, c := twoCrowds(t), compile(t, base)
	for _, id := range []string{"nope", ""} {
		if _, err := Train(context.Background(), st, c, id, now); err == nil {
			t.Errorf("Train(%q) succeeded", id)
		}
	}
	if _, err := st.Model(context.Background(), "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Error("a refused run left a model behind")
	}
}

func TestTrainAllReportsWhatItCannotTrain(t *testing.T) {
	ctx := context.Background()
	c := compile(t, base)

	empty := memory.New() // a catalogue with no interactions yet
	_ = empty.UpsertEntities(ctx, []domain.Entity{film(1), film(2)})
	results, err := TrainAll(ctx, empty, c, now)
	if err != nil || len(results) != 1 {
		t.Fatalf("TrainAll = %v, %v", results, err)
	}
	if results[0].Error == "" || results[0].Version != 0 || !strings.Contains(results[0].Error, "no interactions") {
		t.Errorf("an untrainable signal must say why and save nothing: %+v", results[0])
	}
	if _, err := empty.Model(ctx, "taste"); !errors.Is(err, store.ErrNotFound) {
		t.Error("a failed run saved a model")
	}

	full := twoCrowds(t)
	results, err = TrainAll(ctx, full, c, now)
	if err != nil || len(results) != 1 || results[0].Error != "" || results[0].Version != 1 {
		t.Errorf("TrainAll = %+v, %v", results, err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := TrainAll(cancelled, full, c, now); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled run returned %v, want context.Canceled", err)
	}

	noEmbedding := compile(t, strings.Replace(base, "type: embedding, for: film, factors: 4, regularization: 0.1, alpha: 10, iterations: 10,", "type: global_count,", 1))
	if results, _ := TrainAll(ctx, full, noEmbedding, now); len(results) != 0 {
		t.Errorf("a schema with no embedding trained %v", results)
	}
}

func TestAFailedRunKeepsTheActiveModel(t *testing.T) {
	ctx := context.Background()
	st, c := twoCrowds(t), compile(t, base)
	if _, err := Train(ctx, st, c, "taste", now); err != nil {
		t.Fatal(err)
	}
	// the interactions are gone, as after a data reset: the next run has nothing to learn from
	empty := memory.New()
	_, _ = empty.SaveModel(ctx, "taste", mustModel(t, st))
	if _, err := Train(ctx, empty, c, "taste", now); !errors.Is(err, factors.ErrNoData) {
		t.Fatalf("Train = %v, want ErrNoData", err)
	}
	if m, err := empty.Model(ctx, "taste"); err != nil || m.Version != 1 {
		t.Errorf("the active model after a failed run = %+v, %v; want version 1 untouched", m, err)
	}
}

func mustModel(t *testing.T, st store.Store) *factors.Model {
	t.Helper()
	m, err := st.Model(context.Background(), "taste")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestStatusSaysWhenAModelIsStale(t *testing.T) {
	ctx := context.Background()
	st, c := twoCrowds(t), compile(t, base)

	states, err := Status(ctx, st, c)
	if err != nil || len(states) != 1 {
		t.Fatalf("Status = %v, %v", states, err)
	}
	if s := states[0]; s.Trained || !s.RetrainPending || s.Reason != "not trained yet" || s.Wanted.Factors != 4 || s.Settings != nil {
		t.Errorf("before training: %+v", s)
	}

	if _, err := Train(ctx, st, c, "taste", now); err != nil {
		t.Fatal(err)
	}
	s := mustStatus(t, st, c)
	if !s.Trained || s.RetrainPending || s.Version != 1 || s.Items != 12 || s.Users != 30 || s.Settings.Factors != 4 || s.TrainedAt == nil {
		t.Errorf("after training: %+v", s)
	}

	// T1 changes mark the model stale; the active model is untouched and keeps serving
	for name, text := range map[string]string{
		"factors":        strings.Replace(base, "factors: 4", "factors: 8", 1),
		"regularization": strings.Replace(base, "regularization: 0.1", "regularization: 0.5", 1),
		"alpha":          strings.Replace(base, "alpha: 10", "alpha: 20", 1),
		"iterations":     strings.Replace(base, "iterations: 10", "iterations: 12", 1),
		"of":             strings.Replace(base, "iterations: 10,", "iterations: 10, of: [watch],", 1),
	} {
		s := mustStatus(t, st, compile(t, text))
		if !s.RetrainPending || s.Reason == "" || !s.Trained || s.Version != 1 {
			t.Errorf("after changing %s: %+v", name, s)
		}
	}
	// T3 changes do not
	for name, text := range map[string]string{
		"weight": strings.Replace(base, "default: 0.5", "default: 0.9", 1),
		"label":  strings.Replace(base, "default: 0.5", "default: 0.5, label: Hidden taste", 1),
	} {
		if s := mustStatus(t, st, compile(t, text)); s.RetrainPending {
			t.Errorf("changing the %s must not ask for a retrain: %+v", name, s)
		}
	}
}

func mustStatus(t *testing.T, st store.Store, c *schema.Compiled) State {
	t.Helper()
	states, err := Status(context.Background(), st, c)
	if err != nil || len(states) != 1 {
		t.Fatalf("Status = %v, %v", states, err)
	}
	return states[0]
}

// newEmptyStore is a catalogue of films with nobody having watched anything yet.
func newEmptyStore(t *testing.T) store.Store {
	t.Helper()
	st := memory.New()
	if err := st.UpsertEntities(context.Background(), []domain.Entity{film(1), film(2), film(3)}); err != nil {
		t.Fatal(err)
	}
	return st
}
