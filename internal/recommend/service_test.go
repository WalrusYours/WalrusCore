package recommend

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
)

func exampleText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "schema", "schema-examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeRanker records what the request layer resolved and returns a fixed list.
type fakeRanker struct {
	got   RankInput
	calls int
	items []domain.ScoredItem
}

func (f *fakeRanker) Rank(_ context.Context, in RankInput) (RankOutput, error) {
	f.got, f.calls = in, f.calls+1
	reasons := map[domain.EntityID]string{}
	for _, it := range f.items {
		reasons[it.Item] = "because " + string(it.Item)
	}
	return RankOutput{Items: f.items, Reasons: reasons, Candidates: 42}, nil
}

func newService(t *testing.T, yamlText string, r Ranker) *Service {
	t.Helper()
	svc := schema.NewService()
	if res := svc.Load([]byte(yamlText), schema.LoadOptions{Author: "test"}); !res.OK {
		t.Fatalf("schema rejected: %+v", res.Errors)
	}
	return NewService(svc, r)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func code(err error) string {
	if e, ok := err.(*Error); ok {
		return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprint(err)
}

func scored(ids ...string) []domain.ScoredItem {
	out := make([]domain.ScoredItem, len(ids))
	for i, id := range ids {
		out[i] = domain.ScoredItem{Item: domain.EntityID(id), Score: 1 - float64(i)/10,
			Signals: map[string]float64{"co_listed": 0.3 - float64(i)/100, "sounds_like": 0.1}}
	}
	return out
}

func TestNoSchemaYet(t *testing.T) {
	s := NewService(schema.NewService(), nil)
	_, err := s.Recommend(context.Background(), Request{Recommender: "default", User: "u"})
	if e, ok := err.(*Error); !ok || e.Status != 409 || e.Code != "schema_missing" {
		t.Fatalf("got %v", err)
	}
}

func TestUnknownRecommender(t *testing.T) {
	s := newService(t, exampleText(t, "playlist.yml"), nil)
	_, err := s.Recommend(context.Background(), Request{Recommender: "nope", Items: []string{"a"}})
	if e, ok := err.(*Error); !ok || e.Status != 404 || e.Code != "unknown_recommender" {
		t.Fatalf("got %v", err)
	}
	// a schema that declares recommenders has no implicit `default`, and says so
	_, err = s.Recommend(context.Background(), Request{Recommender: "default", User: "u"})
	if e, ok := err.(*Error); !ok || e.Code != "unknown_recommender" || !strings.Contains(e.Message, "by name") {
		t.Fatalf("got %v", err)
	}
}

func TestImplicitDefaultRecommender(t *testing.T) {
	f := &fakeRanker{items: scored("p1", "p2")}
	s := newService(t, exampleText(t, "feed.yml"), f)
	r, err := s.Recommend(context.Background(), Request{Recommender: "default", User: "mara"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Recommender != "default" || r.Used != "default" || len(r.Items) != 2 || r.Items[0].Type != "post" {
		t.Fatalf("response = %+v", r)
	}
	if f.got.Spec.EffectiveSeed() != schema.SeedUser || f.got.Seed.User != "mara" {
		t.Errorf("the implicit recommender is seed user: %+v", f.got.Seed)
	}
	if len(f.got.Weights) != 7 { // every signal of feed.yml
		t.Errorf("weights for every signal, got %v", f.got.Weights)
	}
	if !near(f.got.Weights["content"], 0.5) || !near(f.got.Weights["author_spread"], 0.4) {
		t.Errorf("weights = %v (default preset: taste 0.5, explore 0.2)", f.got.Weights)
	}
	if r.RecID == "" || !strings.HasPrefix(r.RecID, "rec_") {
		t.Errorf("rec_id = %q", r.RecID)
	}
}

func TestSeedValidation(t *testing.T) {
	s := newService(t, exampleText(t, "playlist.yml"), &fakeRanker{})
	for _, c := range []struct {
		name    string
		req     Request
		contain string // "" means it must succeed
	}{
		{"the right seed", Request{Items: []string{"a", "b"}}, ""},
		{"user beside the seed is fine", Request{Items: []string{"a"}, User: "u1"}, ""},
		{"an empty list is a seed", Request{Items: []string{}}, ""},
		{"no seed at all", Request{}, "needs items"},
		{"only a user", Request{User: "u1"}, "needs items, not [user]"},
		{"a single item", Request{Item: "a"}, "takes items, not item"},
		{"two seed types", Request{Items: []string{"a"}, Item: "b"}, "takes items, not item"},
		{"too many songs", Request{Items: make([]string, maxSeedItems+1)}, "at most"},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.req.Recommender = "playlist_add"
			_, err := s.Recommend(context.Background(), c.req)
			switch {
			case c.contain == "" && err != nil:
				t.Errorf("want success, got %s", code(err))
			case c.contain != "" && (err == nil || !strings.Contains(code(err), c.contain)):
				t.Errorf("want an error containing %q, got %v", c.contain, err)
			case c.contain != "" && !strings.HasPrefix(code(err), "400"):
				t.Errorf("want a 400, got %s", code(err))
			}
		})
	}
}

func TestPlaylistWeightsAndKnobs(t *testing.T) {
	f := &fakeRanker{items: scored("x")}
	s := newService(t, exampleText(t, "playlist.yml"), f)
	run := func(req Request) *Response {
		t.Helper()
		req.Recommender, req.Items = "playlist_add", []string{"a", "b", "c"}
		r, err := s.Recommend(context.Background(), req)
		if err != nil {
			t.Fatal(code(err))
		}
		return r
	}

	r := run(Request{})
	// the knobs' defaults (vibe 0.3, hits 0.3) drive sounds_like, exploration, popularity
	for sig, want := range map[string]float64{
		"sounds_like": 0.4 * 0.7, "exploration": 0.3 * 0.3, "popularity": 0.2 * 0.3,
		"co_listed": 0.4, "genre_fit": 0.2,
		"energy_fit": 0.1 * 3 / 5, // a weight that depends on seed.size
	} {
		if !near(r.Weights[sig], want) {
			t.Errorf("%s = %v, want %v", sig, r.Weights[sig], want)
		}
	}
	if _, has := r.Weights["title_match"]; has {
		t.Error("title_match is not one of playlist_add's signals")
	}

	// a Tune override moves the weights it drives and nothing else
	r = run(Request{Knobs: map[string]float64{"vibe_vs_branch_out": 1}})
	if !near(r.Weights["sounds_like"], 0) || !near(r.Weights["exploration"], 0.3) || !near(r.Weights["co_listed"], 0.4) {
		t.Errorf("branch out: %v", r.Weights)
	}
	if !near(r.Knobs["vibe_vs_branch_out"], 1) {
		t.Errorf("resolved knobs = %v", r.Knobs)
	}
	// a bigger playlist trusts its centre more
	f.got = RankInput{}
	if _, err := s.Recommend(context.Background(), Request{Recommender: "playlist_add", Items: make([]string, 9)}); err != nil {
		t.Fatal(err)
	}
	if !near(f.got.Weights["energy_fit"], 0.1) {
		t.Errorf("energy_fit with 9 songs = %v, want 0.1 (capped at 5)", f.got.Weights["energy_fit"])
	}

	// bad knobs
	for _, bad := range []map[string]float64{{"nope": 0.5}} {
		_, err := s.Recommend(context.Background(), Request{Recommender: "playlist_add", Items: []string{"a"}, Knobs: bad})
		if err == nil || !strings.Contains(code(err), "unknown knob") {
			t.Errorf("want unknown knob, got %v", err)
		}
	}
	_, err := s.Recommend(context.Background(), Request{Recommender: "playlist_add", Items: []string{"a"}, Preset: "nope"})
	if err == nil || !strings.HasPrefix(code(err), "400") {
		t.Errorf("unknown preset: %v", err)
	}
}

// A knob outside the recommender's `knobs:` neither accepts overrides nor moves its weights, and a
// recommender weight applies only to a signal no in-scope knob drives (spotify.yml).
func TestKnobScopeAndRecommenderWeights(t *testing.T) {
	f := &fakeRanker{}
	s := newService(t, exampleText(t, "spotify.yml"), f)
	ask := func(items int, knobs map[string]float64) (*Response, error) {
		return s.Recommend(context.Background(), Request{Recommender: "playlist_add", Items: make([]string, items), Knobs: knobs})
	}

	// taste_vs_crowd is a feed knob: not offered by playlist_add
	if _, err := ask(3, map[string]float64{"taste_vs_crowd": 1}); err == nil || !strings.Contains(code(err), "not offered by playlist_add") {
		t.Fatalf("want 'not offered', got %v", err)
	}
	if _, err := ask(3, map[string]float64{"vibe_vs_branch_out": 0.5}); err != nil {
		t.Fatal(code(err))
	}

	r, _ := ask(3, nil)
	// `collaborative` is driven only by taste_vs_crowd, which is out of scope, so the recommender's
	// own weight expression applies: 0.15 + 0.25 * max(0, 3 - seed.size) / 3
	if !near(r.Weights["collaborative"], 0.15) {
		t.Errorf("collaborative with 3 songs = %v, want 0.15", r.Weights["collaborative"])
	}
	r, _ = ask(1, nil)
	if !near(r.Weights["collaborative"], 0.15+0.25*2/3) {
		t.Errorf("collaborative with 1 song = %v, want %v", r.Weights["collaborative"], 0.15+0.25*2/3)
	}
	// `sounds_like` IS driven by an in-scope knob: the knob's default wins (0.35 * (1 - 0.3))
	if !near(r.Weights["sounds_like"], 0.35*0.7) {
		t.Errorf("sounds_like = %v, want %v", r.Weights["sounds_like"], 0.35*0.7)
	}
	// meta-parameters come from in-scope knobs only
	if !near(f.got.Meta["recommenders.playlist_add.blend_user"], 0.15) {
		t.Errorf("blend_user = %v", f.got.Meta["recommenders.playlist_add.blend_user"])
	}
}

func TestContext(t *testing.T) {
	f := &fakeRanker{}
	s := newService(t, exampleText(t, "shop.yml"), f)
	s.now = func() time.Time { return time.Date(2026, 10, 3, 21, 30, 0, 0, time.UTC) } // a Saturday
	ask := func(cx map[string]any) error {
		_, err := s.Recommend(context.Background(), Request{Recommender: "home", User: "u", Context: cx})
		return err
	}

	if err := ask(map[string]any{"device": "phone"}); err != nil {
		t.Fatal(code(err))
	}
	if h, _ := f.got.Context["hour"].AsFloat(); h != 21 {
		t.Errorf("hour is derived from the request time: %v", f.got.Context["hour"])
	}
	for _, c := range []struct {
		name    string
		cx      map[string]any
		contain string
	}{
		{"required field missing", nil, "context.device is required"},
		{"unknown field", map[string]any{"device": "phone", "mood": "x"}, "unknown_context_field"},
		{"value not allowed", map[string]any{"device": "fridge"}, "is not one of"},
		{"wrong type", map[string]any{"device": 3.0}, "expected a string"},
		{"derived field sent", map[string]any{"device": "phone", "hour": 3.0}, "derived from the request time"},
		{"set of the wrong shape", map[string]any{"device": "phone", "pinned": "a"}, "list of strings"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := ask(c.cx); err == nil || !strings.Contains(code(err), c.contain) || !strings.HasPrefix(code(err), "400") {
				t.Errorf("want a 400 containing %q, got %v", c.contain, err)
			}
		})
	}
}

func TestLimitExcludeAndReasons(t *testing.T) {
	f := &fakeRanker{items: scored("a", "b", "c", "d", "e")}
	s := newService(t, exampleText(t, "playlist.yml"), f)
	ask := func(req Request) *Response {
		req.Recommender, req.Items = "playlist_add", []string{"s"}
		r, err := s.Recommend(context.Background(), req)
		if err != nil {
			t.Fatal(code(err))
		}
		return r
	}

	if r := ask(Request{}); f.got.Limit != 20 || len(r.Items) != 5 {
		t.Errorf("default limit = %d, items = %d", f.got.Limit, len(r.Items))
	}
	if ask(Request{Limit: 500}); f.got.Limit != 50 {
		t.Errorf("limit above max is clamped to 50, got %d", f.got.Limit)
	}
	r := ask(Request{Limit: 2, Exclude: []string{"a"}})
	if len(r.Items) != 2 || r.Items[0].ID != "b" || r.Items[1].ID != "c" {
		t.Errorf("exclude then limit: %+v", r.Items)
	}
	if !f.got.Exclude["a"] {
		t.Error("the ranker is told what to exclude")
	}
	if r.Items[0].Reason != "" {
		t.Error("reasons only with explain: true")
	}
	if r = ask(Request{Limit: 1, Explain: true}); r.Items[0].Reason != "because a" {
		t.Errorf("reason = %q", r.Items[0].Reason)
	}
	if r.CandidatesConsidered != 42 {
		t.Errorf("candidates = %d", r.CandidatesConsidered)
	}
}

func TestFallbackToTheTitle(t *testing.T) {
	f := &fakeRanker{items: scored("x")}
	s := newService(t, exampleText(t, "playlist.yml"), f)
	r, err := s.Recommend(context.Background(), Request{
		Recommender: "playlist_add", Items: []string{},
		Context: map[string]any{"title_words": []any{"rock", "workout"}},
	})
	if err != nil {
		t.Fatal(code(err))
	}
	if r.Recommender != "playlist_add" || r.Used != "from_title" || f.got.Recommender != "from_title" {
		t.Errorf("an empty playlist is answered by from_title: %+v", r)
	}
	if _, has := r.Weights["title_match"]; !has || len(r.Weights) != 2 {
		t.Errorf("from_title's signals: %v", r.Weights)
	}
	if got, _ := f.got.Context["title_words"].AsSet(); len(got) != 2 {
		t.Errorf("the ranker gets the context: %v", f.got.Context)
	}

	// a playlist with songs is not a fallback
	if r, _ = s.Recommend(context.Background(), Request{Recommender: "playlist_add", Items: []string{"a"}}); r.Used != "playlist_add" {
		t.Errorf("used = %q", r.Used)
	}
}

func TestExplainByRecID(t *testing.T) {
	f := &fakeRanker{items: scored("a", "b")}
	s := newService(t, exampleText(t, "playlist.yml"), f)
	r, err := s.Recommend(context.Background(), Request{Recommender: "playlist_add", Items: []string{"s"}, Explain: true})
	if err != nil {
		t.Fatal(code(err))
	}

	b, err := s.ExplainRec(r.RecID, "b")
	if err != nil {
		t.Fatal(code(err))
	}
	if b.Position != 2 || b.Item != "b" || b.Used != "playlist_add" || b.Reason != "because b" {
		t.Errorf("breakdown = %+v", b)
	}
	if len(b.Breakdown) != 2 || b.Breakdown[0].Signal != "co_listed" || b.Breakdown[0].Value < b.Breakdown[1].Value {
		t.Errorf("signals best first: %+v", b.Breakdown)
	}
	if !near(b.WeightsUsed["co_listed"], 0.4) {
		t.Errorf("weights used = %v", b.WeightsUsed)
	}

	if _, err = s.ExplainRec(r.RecID, "zzz"); err == nil || !strings.Contains(code(err), "404 unknown_item") {
		t.Errorf("item not in the list: %v", err)
	}
	if _, err = s.ExplainRec("rec_nope", "a"); err == nil || !strings.Contains(code(err), "404 unknown_recommendation") {
		t.Errorf("unknown rec_id: %v", err)
	}
	// lists are kept for a minute
	s.results.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err = s.ExplainRec(r.RecID, "a"); err == nil || !strings.Contains(code(err), "unknown_recommendation") {
		t.Errorf("expired list: %v", err)
	}
}

func TestResultCacheIsBounded(t *testing.T) {
	c := newResultCache(3, time.Minute)
	for i := 0; i < 5; i++ {
		c.put(&stored{RecID: fmt.Sprint("r", i), At: time.Now()})
	}
	if _, ok := c.get("r0"); ok {
		t.Error("the oldest entries are dropped")
	}
	if _, ok := c.get("r4"); !ok {
		t.Error("the newest is kept")
	}
}
