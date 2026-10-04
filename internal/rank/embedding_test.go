package rank

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/ingest"
	"github.com/timurcravtov/walrus/internal/learn"
	"github.com/timurcravtov/walrus/internal/recommend"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store/memory"
)

const embeddingSchema = `version: 1
meta: { name: Reel, locales: [en], default_locale: en }
entities:
  user: { key: id }
  film:
    key: id
    attributes:
      title: { type: string }
interactions:
  watch: { kind: positive, weight: 2, half_life: 60d }
signals:
  taste:      { type: embedding, for: film, factors: 2, regularization: 0.1, alpha: 10, iterations: 15, default: 0.5 }
  popularity: { type: global_count, normalise: rank, default: 0.1 }
knobs:
  - id: memory
    label: Short memory <-> Long memory
    range: [0, 1]
    default: 0.5
    maps: { interactions.half_life_scale: "lerp(0.05, 20, x)" }
  - id: hidden_taste
    label: Hidden taste
    range: [0, 1]
    default: 0.5
    maps: { taste: "x" }
recommenders:
  home:
    for: [film]
    seed: user
    candidates:
      - { source: factors, signal: taste, cap: 50 }
      - { source: popular, cap: 50 }
    signals: [taste, popularity]
  only_factors:
    for: [film]
    seed: user
    candidates:
      - { source: factors, signal: taste, cap: 50 }
    signals: [taste]
  small_pool:
    for: [film]
    seed: user
    candidates:
      - { source: factors, signal: taste, cap: 3 }
    signals: [taste]
  similar:
    for: [film]
    seed: item
    candidates:
      - { source: factors, signal: taste, cap: 50 }
    signals: [taste]
recommendable: film
`

// Films f00-f05 are one crowd's taste and f06-f11 the other's, in the order the ids sort.
func crowd(id string) string {
	var n int
	_, _ = fmt.Sscanf(id, "Film %d", &n)
	if n < 6 {
		return "A"
	}
	return "B"
}

type embFixture struct {
	t     *testing.T
	ctx   context.Context
	st    *memory.InMemoryStore
	sch   *schema.Service
	ing   *ingest.Service
	ranks *Ranker
	svc   *recommend.Service
	now   time.Time
}

// newEmbFixture has 12 films and 40 viewers: 20 watch four of films 0-5, 20 watch four of 6-11.
// Nothing is trained yet.
func newEmbFixture(t *testing.T) *embFixture {
	t.Helper()
	f := &embFixture{t: t, ctx: context.Background(), st: memory.New(), sch: schema.NewService(), now: time.Now().UTC()}
	if res := f.sch.Load([]byte(embeddingSchema), schema.LoadOptions{Author: "test"}); !res.OK {
		t.Fatalf("schema rejected: %+v", res.Errors)
	}
	f.ing = ingest.NewService(f.sch, f.st)
	f.ranks = New(f.st)
	f.svc = recommend.NewService(f.sch, f.ranks).WithHistory(f.ranks)

	var films []ingest.Raw
	for i := 0; i < 12; i++ {
		films = append(films, ingest.Raw{Entity: "film", ID: fmt.Sprintf("f%02d", i), Attributes: map[string]any{"title": fmt.Sprintf("Film %02d", i)}})
	}
	if res, err := f.ing.Entities(f.ctx, films); err != nil || len(res.Rejected) > 0 {
		t.Fatalf("films: %+v %v", res, err)
	}
	rng := rand.New(rand.NewPCG(3, 8))
	for u := 0; u < 40; u++ {
		first := 0
		if u >= 20 {
			first = 6
		}
		for k, pick := range rng.Perm(6)[:4] {
			f.watch(fmt.Sprintf("viewer%02d", u), first+pick, time.Duration(u+k)*time.Hour)
		}
	}
	return f
}

// watch records that a user watched film n, ago before now.
func (f *embFixture) watch(user string, film int, ago time.Duration) {
	f.t.Helper()
	res, err := f.ing.Interactions(f.ctx, []ingest.RawInteraction{{
		User: user, Type: "watch", Target: fmt.Sprintf("f%02d", film), TS: f.now.Add(-ago).Format(time.RFC3339),
	}})
	if err != nil || len(res.Rejected) > 0 {
		f.t.Fatalf("watch: %+v %v", res, err)
	}
}

func (f *embFixture) train() {
	f.t.Helper()
	if res, err := learn.Train(f.ctx, f.st, f.sch.Compiled(), "taste", f.now); err != nil {
		f.t.Fatalf("train: %+v %v", res, err)
	}
}

func (f *embFixture) ask(req recommend.Request) *recommend.Response {
	f.t.Helper()
	if req.Recommender == "" {
		req.Recommender = "home"
	}
	req.Explain = true
	res, err := f.svc.Recommend(f.ctx, req)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func titles(r *recommend.Response, n int) []string {
	var out []string
	for _, it := range r.Items[:min(n, len(r.Items))] {
		var k int
		_, _ = fmt.Sscanf(it.ID, "f%d", &k)
		out = append(out, fmt.Sprintf("Film %02d", k))
	}
	return out
}

func allCrowd(names []string, want string) bool {
	for _, n := range names {
		if crowd(n) != want {
			return false
		}
	}
	return len(names) > 0
}

func TestEmbeddingRecommendsTheUsersOwnCrowd(t *testing.T) {
	f := newEmbFixture(t)
	f.train()

	// a user the model never saw, whose history is two films of the first crowd
	f.watch("newbie", 1, time.Hour)
	f.watch("newbie", 2, 2*time.Hour)

	res := f.ask(recommend.Request{User: "newbie", Limit: 6})
	top := titles(res, 4)
	if !allCrowd(top, "A") {
		t.Errorf("the first four should be films of the first crowd (the user's), got %v", top)
	}
	for _, seen := range []string{"f01", "f02"} {
		if slices.Contains(ids(res), seen) {
			t.Errorf("%s is already in the history and must not be recommended", seen)
		}
	}
	reason := res.Items[0].Reason
	if !strings.Contains(reason, "Because you interacted with") || !strings.Contains(reason, "Film 01") && !strings.Contains(reason, "Film 02") {
		t.Errorf("the reason should say which of their films it follows: %q", reason)
	}
	if strings.Contains(reason, "{") {
		t.Errorf("an unfilled placeholder: %q", reason)
	}
}

func TestEmbeddingFollowsNewActivityWithoutRetraining(t *testing.T) {
	f := newEmbFixture(t)
	f.train()

	// no history yet: the signal says nothing, so every item gets the same share from it
	before := f.ask(recommend.Request{User: "late", Recommender: "only_factors", Limit: 12})
	for _, it := range before.Items {
		if math.Abs(it.Score-before.Items[0].Score) > 1e-9 {
			t.Fatalf("a user with no history must score every film alike: %+v", before.Items)
		}
	}

	// a minute later the user watched two films of the second crowd; the model is not retrained
	f.watch("late", 7, time.Minute)
	f.watch("late", 9, time.Minute)
	after := f.ask(recommend.Request{User: "late", Limit: 6})
	if top := titles(after, 3); !allCrowd(top, "B") {
		t.Errorf("recent activity must steer the very next request, got %v", top)
	}
}

func TestEmbeddingForAnItemSeedIsWhatPeopleWhoPickedItAlsoPick(t *testing.T) {
	f := newEmbFixture(t)
	f.train()

	res := f.ask(recommend.Request{Recommender: "similar", Item: "f08", Limit: 5})
	if top := titles(res, 4); !allCrowd(top, "B") {
		t.Errorf("films like a second-crowd film should be second-crowd films: %v", top)
	}
	if slices.Contains(ids(res), "f08") {
		t.Error("the seed itself must not be recommended")
	}
	if reason := res.Items[0].Reason; !strings.Contains(reason, "Film 08") || !strings.Contains(reason, "often pick this too") {
		t.Errorf("reason = %q", reason)
	}
}

func TestTheHorizonKnobMovesTheEmbedding(t *testing.T) {
	f := newEmbFixture(t)
	f.train()

	f.switcher()

	short := f.ask(recommend.Request{User: "switcher", Limit: 3, Knobs: map[string]float64{"memory": 0}})
	long := f.ask(recommend.Request{User: "switcher", Limit: 3, Knobs: map[string]float64{"memory": 1}})
	if top := titles(short, 2); !allCrowd(top, "A") {
		t.Errorf("a short memory forgets the old films, so today's taste leads: %v", top)
	}
	if top := titles(long, 2); !allCrowd(top, "B") {
		t.Errorf("a long memory remembers the four old films, so their crowd leads: %v", top)
	}
	// the pool is the same either way; only the order moved
	if short.CandidatesConsidered != long.CandidatesConsidered {
		t.Errorf("candidates changed with the knob: %d vs %d", short.CandidatesConsidered, long.CandidatesConsidered)
	}
}

// switcher watched four second-crowd films eight months ago (four half-lives) and one first-crowd
// film today. With the schema's own half-lives today's film dominates; with a long memory the four
// old ones do.
func (f *embFixture) switcher() {
	f.t.Helper()
	for _, film := range []int{6, 7, 8, 9} {
		f.watch("switcher", film, 240*24*time.Hour)
	}
	f.watch("switcher", 0, time.Minute)
}

// The candidate source reads the schema's own half-lives, never the request's memory knob, so
// moving the slider cannot change which films are in the pool, only how they are ordered.
func TestThePoolIsBuiltFromTheSchemasOwnMemoryNotTheKnobs(t *testing.T) {
	f := newEmbFixture(t)
	f.train()
	f.switcher()

	// a pool of three, smaller than the catalogue, so which three is a real choice
	pool := func(memory float64) []string {
		res := f.ask(recommend.Request{User: "switcher", Recommender: "small_pool", Limit: 12, Knobs: map[string]float64{"memory": memory}})
		got := ids(res)
		slices.Sort(got)
		return got
	}
	short, long := pool(0), pool(1)
	if !slices.Equal(short, long) {
		t.Errorf("the pool moved with the knob: %v vs %v", short, long)
	}
	if len(short) != 3 {
		t.Fatalf("setup: the pool should hold 3 films, got %v", short)
	}
	// by the schema's own half-lives today's first-crowd film dominates, so its crowd fills the pool;
	// a long-memory fit would have picked the second crowd's two unseen films instead
	for _, id := range short {
		if id == "f10" || id == "f11" {
			t.Errorf("the pool followed the long memory, not the schema's half-lives: %v", short)
		}
	}
}

func TestTheFactorsSourceDoesNotDependOnWeights(t *testing.T) {
	f := newEmbFixture(t)
	f.train()
	f.watch("ann", 3, time.Hour)
	f.watch("ann", 4, time.Hour)

	off := f.ask(recommend.Request{User: "ann", Recommender: "only_factors", Limit: 12, Knobs: map[string]float64{"hidden_taste": 0}})
	on := f.ask(recommend.Request{User: "ann", Recommender: "only_factors", Limit: 12, Knobs: map[string]float64{"hidden_taste": 1}})
	if off.CandidatesConsidered == 0 || off.CandidatesConsidered != on.CandidatesConsidered {
		t.Errorf("candidates with the weight off %d, on %d; they must not depend on weights", off.CandidatesConsidered, on.CandidatesConsidered)
	}
	if got := ids(on); !allCrowd(titles(on, 3), "A") || slices.Contains(got, "f03") {
		t.Errorf("the source should offer the user's crowd, without what they watched: %v", got)
	}
}

func TestWithoutATrainedModelItStillRanksAndSaysNothing(t *testing.T) {
	f := newEmbFixture(t)
	f.watch("ann", 3, time.Hour)

	res := f.ask(recommend.Request{User: "ann", Limit: 12})
	if len(res.Items) == 0 {
		t.Fatal("the other candidate source should still supply a pool")
	}
	// the untrained signal is the same for every film, so it cannot decide the order
	var shares []float64
	for _, it := range res.Items {
		b, err := f.svc.ExplainRec(res.RecID, it.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range b.Breakdown {
			if s.Signal == "taste" {
				shares = append(shares, s.Value)
			}
		}
	}
	if len(shares) != len(res.Items) {
		t.Fatalf("every film should carry a taste share, got %d of %d", len(shares), len(res.Items))
	}
	for _, v := range shares {
		if math.Abs(v-shares[0]) > 1e-9 {
			t.Errorf("an untrained signal must be constant: %v", shares)
			break
		}
	}
}

func TestTrainingInvalidatesWhatWasCached(t *testing.T) {
	f := newEmbFixture(t)
	f.watch("ann", 3, time.Hour)
	f.watch("ann", 4, time.Hour)

	untrained := f.ask(recommend.Request{User: "ann", Recommender: "only_factors", Limit: 12})
	builds := f.ranks.builds.Load()
	f.ask(recommend.Request{User: "ann", Recommender: "only_factors", Limit: 12})
	if f.ranks.builds.Load() != builds {
		t.Fatal("the same request twice should reuse the snapshot")
	}

	f.train() // saving a model changes the store version
	trained := f.ask(recommend.Request{User: "ann", Recommender: "only_factors", Limit: 12})
	if f.ranks.builds.Load() == builds {
		t.Error("a new model must not be served from a snapshot built before it")
	}
	if len(untrained.Items) != 0 || len(trained.Items) == 0 {
		t.Errorf("with only the factors source, no model means no candidates (%d) and a model means some (%d)", len(untrained.Items), len(trained.Items))
	}
}

func TestAnItemTheModelHasNotSeenIsNeitherFavouredNorHeldBack(t *testing.T) {
	f := newEmbFixture(t)
	f.train()

	// a film added after training, which one viewer has already watched, so it is a popular candidate
	if res, err := f.ing.Entities(f.ctx, []ingest.Raw{{Entity: "film", ID: "f99", Attributes: map[string]any{"title": "Film 99"}}}); err != nil || len(res.Rejected) > 0 {
		t.Fatalf("film: %+v %v", res, err)
	}
	f.watch("viewer02", 99, time.Hour)
	f.watch("ann", 2, time.Hour)
	f.watch("ann", 3, time.Hour)
	res := f.ask(recommend.Request{User: "ann", Limit: 20})

	if !slices.Contains(ids(res), "f99") {
		t.Fatalf("setup: f99 was watched, so it should be a popular candidate: %v", ids(res))
	}
	b, err := f.svc.ExplainRec(res.RecID, "f99")
	if err != nil {
		t.Fatal(err)
	}
	var share float64 = -1
	for _, s := range b.Breakdown {
		if s.Signal == "taste" {
			share = s.Value
		}
	}
	// the weight 0.5 times the neutral 0.5: no information either way
	if math.Abs(share-0.25) > 1e-9 {
		t.Errorf("a film the model has not seen should get the neutral share 0.25, got %v", share)
	}
	// and the explanation does not claim anything about it
	for _, it := range res.Items {
		if it.ID == "f99" && strings.Contains(it.Reason, "Because you interacted") {
			t.Errorf("no model data, so no 'because': %q", it.Reason)
		}
	}
}

func TestNormaliseLeavesOutWhatHasNoValue(t *testing.T) {
	nan := math.NaN()
	got := normalise([]float64{1, nan, 3, 2}, "")
	want := []float64{0, 0.5, 1, 0.5}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Fatalf("minmax = %v, want %v (the missing value must not stretch the scale)", got, want)
		}
	}
	if r := normalise([]float64{10, nan, 1000, 100}, "rank"); !(r[0] < r[3] && r[3] < r[2]) || r[1] != 0.5 || r[2] != 1 {
		t.Errorf("rank = %v", r)
	}
	if n := normalise([]float64{4, nan}, "none"); n[0] != 4 || n[1] != 0.5 {
		t.Errorf("none = %v", n)
	}
	if c := normalise([]float64{nan, nan}, ""); c[0] != 0.5 || c[1] != 0.5 {
		t.Errorf("all missing = %v", c)
	}
}

func TestEmbeddingIsSupportedByTheRanker(t *testing.T) {
	f := newEmbFixture(t)
	for _, is := range f.ranks.Unsupported(f.sch.Compiled().Schema) {
		t.Errorf("the ranker claims not to support: %s: %s", is.Path, is.Message)
	}
}

// movies.yml ships a learned taste: this trains it on a small catalogue and checks the pieces the
// example promises. At the "my taste" end of its slider the collaborative signals have no weight, so
// the learned taste is what tells the two groups of films apart.
func TestTheMoviesExampleLearnsATasteAndItsSliderReachesIt(t *testing.T) {
	f := engine(t, "movies.yml")
	var raws []ingest.Raw
	for i := 0; i < 12; i++ {
		d := fmt.Sprintf("d%02d", i) // one director each, so the example's quota on directors never bites
		raws = append(raws, ingest.Raw{Entity: "director", ID: d, Attributes: map[string]any{"name": d}}, film(fmt.Sprintf("f%02d", i), d, 2005))
	}
	f.entities(raws)
	rng := rand.New(rand.NewPCG(3, 8))
	var events []ingest.RawInteraction
	for u := 0; u < 40; u++ {
		first := 0
		if u >= 20 {
			first = 6
		}
		for _, pick := range rng.Perm(6)[:4] {
			events = append(events, ingest.RawInteraction{User: fmt.Sprintf("viewer%02d", u), Type: "watchlist", Target: fmt.Sprintf("f%02d", first+pick)})
		}
	}
	f.events(events...)
	f.events(
		ingest.RawInteraction{User: "late", Type: "watchlist", Target: "f07"},
		ingest.RawInteraction{User: "late", Type: "watchlist", Target: "f09"},
	)
	crowdB := func(id string) bool { return id >= "f06" }
	myTaste := map[string]float64{"taste_vs_crowd": 0}

	hiddenShare := func(recID, item string) (float64, string) {
		t.Helper()
		b, err := f.svc.ExplainRec(recID, item)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range b.Breakdown {
			if s.Signal == "hidden_taste" {
				return s.Value, s.Because
			}
		}
		return -1, ""
	}

	// before training the signal says nothing: every film gets the same share from it
	before := f.must(recommend.Request{Recommender: "home", User: "late", Limit: 5, Knobs: myTaste})
	a, _ := hiddenShare(before.RecID, before.Items[0].ID)
	b, _ := hiddenShare(before.RecID, before.Items[len(before.Items)-1].ID)
	if a != b || a <= 0 {
		t.Errorf("an untrained hidden_taste should give every film the same share, got %v and %v", a, b)
	}

	if res, err := learn.Train(context.Background(), f.ranks.store, f.sch.Compiled(), "hidden_taste", time.Now()); err != nil {
		t.Fatalf("train: %+v %v", res, err)
	}

	res := f.must(recommend.Request{Recommender: "home", User: "late", Limit: 5, Knobs: myTaste})
	for _, it := range res.Items[:3] {
		if !crowdB(it.ID) {
			t.Errorf("a viewer of two second-group films should be led to that group: %v", ids(res))
			break
		}
	}
	if res.Weights["hidden_taste"] <= 0 {
		t.Errorf("at the 'my taste' end the learned taste should count: %v", res.Weights)
	}
	if share, because := hiddenShare(res.RecID, res.Items[0].ID); share <= 0 || !strings.Contains(because, "Because you interacted with") {
		t.Errorf("the top film's learned-taste share = %v, because = %q", share, because)
	}

	// the other end of the slider gives it no weight: it is the crowd's turn
	crowd := f.must(recommend.Request{Recommender: "home", User: "late", Limit: 5, Knobs: map[string]float64{"taste_vs_crowd": 1}})
	if crowd.Weights["hidden_taste"] != 0 {
		t.Errorf("at the 'what everyone watches' end the learned taste should have no weight: %v", crowd.Weights)
	}

	// "more like this": the films people who picked this one also picked
	similar := f.must(recommend.Request{Recommender: "similar_films", Item: "f08", Limit: 4, Knobs: myTaste})
	for _, it := range similar.Items[:3] {
		if !crowdB(it.ID) {
			t.Errorf("films like a second-group film should come from its group: %v", ids(similar))
			break
		}
	}
	if _, because := hiddenShare(similar.RecID, similar.Items[0].ID); !strings.Contains(because, "often pick this too") {
		t.Errorf("because = %q", because)
	}
}
