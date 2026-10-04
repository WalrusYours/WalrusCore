package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/recommend"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store/memory"
)

// stubRanker returns a fixed list so the HTTP layer can be tested without a store.
type stubRanker struct{ got recommend.RankInput }

func (r *stubRanker) Rank(_ context.Context, in recommend.RankInput) (recommend.RankOutput, error) {
	r.got = in
	return recommend.RankOutput{
		Items: []domain.ScoredItem{
			{Item: "a", Score: 0.9, Signals: map[string]float64{"content": 0.6, "popularity": 0.3}},
			{Item: "b", Score: 0.5, Signals: map[string]float64{"content": 0.5}},
		},
		Reasons:    map[domain.EntityID]string{"a": "because you liked x"},
		Candidates: 7,
	}, nil
}

func newRecommendServer(t *testing.T) (*httptest.Server, *stubRanker) {
	t.Helper()
	ranker := &stubRanker{}
	h := New(Config{
		AdminKey: adminKey, InstanceID: "abc123", InstanceName: "test", Version: "t",
		FailDelay: -1, MaxSchemaBytes: 64 << 10,
	}, Deps{Schema: schema.NewService(), Store: memory.New(), Ranker: ranker})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, ranker
}

func pushExample(t *testing.T, srv *httptest.Server, name string) {
	t.Helper()
	res, body := do(t, srv, "PUT", "/v1/schema", example(t, name), bearer)
	if res.StatusCode != 200 {
		t.Fatalf("push %s = %d %s", name, res.StatusCode, body)
	}
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func wantError(t *testing.T, res *http.Response, body []byte, status int, code string) apiError {
	t.Helper()
	var e apiError
	_ = json.Unmarshal(body, &e)
	if res.StatusCode != status || e.Error.Code != code {
		t.Fatalf("got %d %s, want %d %s", res.StatusCode, body, status, code)
	}
	return e
}

func TestRecommendNeedsAuth(t *testing.T) {
	srv, _ := newRecommendServer(t)
	for _, c := range []struct{ method, path string }{
		{"POST", "/v1/recommenders/playlist_add/recommend"},
		{"GET", "/v1/recommend/mara"},
		{"POST", "/v1/recommend/mara"},
		{"GET", "/v1/recommendations/rec_x/explain/a"},
	} {
		if res, _ := do(t, srv, c.method, c.path, "{}", nil); res.StatusCode != 401 {
			t.Errorf("%s %s without a key = %d, want 401", c.method, c.path, res.StatusCode)
		}
	}
}

func TestRecommendBeforeAnySchemaIs409(t *testing.T) {
	srv, _ := newRecommendServer(t)
	res, body := do(t, srv, "POST", "/v1/recommenders/playlist_add/recommend", `{"items":["a"]}`, bearer)
	wantError(t, res, body, 409, "schema_missing")
}

func TestNamedRecommender(t *testing.T) {
	srv, ranker := newRecommendServer(t)
	pushExample(t, srv, "playlist.yml")
	const path = "/v1/recommenders/playlist_add/recommend"

	res, body := do(t, srv, "POST", path, `{"items":["t1","t2"],"limit":5}`, bearer)
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	var r recommend.Response
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.Recommender != "playlist_add" || r.Used != "playlist_add" || r.RecID == "" || len(r.Items) != 2 {
		t.Fatalf("response = %s", body)
	}
	if r.Items[0].ID != "a" || r.Items[0].Reason != "" {
		t.Errorf("reasons are only returned with explain: true: %+v", r.Items[0])
	}
	if r.CandidatesConsidered != 7 || ranker.got.Limit != 5 || len(ranker.got.Seed.Items) != 2 {
		t.Errorf("candidates=%d limit=%d seed=%v", r.CandidatesConsidered, ranker.got.Limit, ranker.got.Seed.Items)
	}

	// explain: true adds the reasons
	_, body = do(t, srv, "POST", path, `{"items":["t1"],"explain":true}`, bearer)
	r = recommend.Response{}
	_ = json.Unmarshal(body, &r)
	if len(r.Items) == 0 || r.Items[0].Reason != "because you liked x" {
		t.Errorf("explain: true should carry the reason: %s", body)
	}
}

func TestRecommendErrors(t *testing.T) {
	srv, _ := newRecommendServer(t)
	pushExample(t, srv, "playlist.yml")
	const path = "/v1/recommenders/playlist_add/recommend"

	t.Run("unknown recommender", func(t *testing.T) {
		res, body := do(t, srv, "POST", "/v1/recommenders/nope/recommend", `{}`, bearer)
		wantError(t, res, body, 404, "unknown_recommender")
	})
	t.Run("wrong seed", func(t *testing.T) {
		res, body := do(t, srv, "POST", path, `{"item":"t1"}`, bearer)
		e := wantError(t, res, body, 400, "seed_mismatch")
		if e.Error.Message == "" {
			t.Error("the message should say what the recommender takes")
		}
	})
	t.Run("no seed", func(t *testing.T) {
		res, body := do(t, srv, "POST", path, ``, bearer)
		wantError(t, res, body, 400, "seed_mismatch")
	})
	t.Run("unknown context field", func(t *testing.T) {
		res, body := do(t, srv, "POST", path, `{"items":["t1"],"context":{"mood":"happy"}}`, bearer)
		wantError(t, res, body, 400, "unknown_context_field")
	})
	t.Run("unknown knob", func(t *testing.T) {
		res, body := do(t, srv, "POST", path, `{"items":["t1"],"knobs":{"nope":1}}`, bearer)
		wantError(t, res, body, 400, "validation_error")
	})
	t.Run("unknown field in the body", func(t *testing.T) {
		res, body := do(t, srv, "POST", path, `{"items":["t1"],"recommender":"x"}`, bearer)
		wantError(t, res, body, 400, "validation_error")
	})
	t.Run("not json", func(t *testing.T) {
		res, body := do(t, srv, "POST", path, `{`, bearer)
		wantError(t, res, body, 400, "validation_error")
	})
}

func TestV1RecommendOnASchemaWithoutRecommenders(t *testing.T) {
	srv, ranker := newRecommendServer(t)
	pushExample(t, srv, "feed.yml")

	res, body := do(t, srv, "GET", "/v1/recommend/mara?limit=10&preset=default", "", bearer)
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	var r recommend.Response
	_ = json.Unmarshal(body, &r)
	if r.Recommender != "default" || ranker.got.User != "mara" || ranker.got.Limit != 10 {
		t.Errorf("response = %s, user = %q, limit = %d", body, ranker.got.User, ranker.got.Limit)
	}

	res, body = do(t, srv, "POST", "/v1/recommend/mara", `{"exclude":["a"]}`, bearer)
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	r = recommend.Response{}
	_ = json.Unmarshal(body, &r)
	if len(r.Items) != 1 || r.Items[0].ID != "b" {
		t.Errorf("exclude should drop a: %s", body)
	}

	res, body = do(t, srv, "GET", "/v1/recommend/mara?limit=many", "", bearer)
	wantError(t, res, body, 400, "validation_error")
	res, body = do(t, srv, "GET", "/v1/recommend/mara?knobs.nope=1", "", bearer)
	wantError(t, res, body, 400, "validation_error")
	res, body = do(t, srv, "GET", "/v1/recommend/mara?knobs.nope=high", "", bearer)
	wantError(t, res, body, 400, "validation_error")
}

func TestV1RecommendPointsToNamedRecommenders(t *testing.T) {
	srv, _ := newRecommendServer(t)
	pushExample(t, srv, "playlist.yml")
	res, body := do(t, srv, "GET", "/v1/recommend/mara", "", bearer)
	e := wantError(t, res, body, 404, "unknown_recommender")
	if e.Error.Message != "this schema declares its own recommenders; call one by name" {
		t.Errorf("message = %q", e.Error.Message)
	}
}

func TestExplainByRecID(t *testing.T) {
	srv, _ := newRecommendServer(t)
	pushExample(t, srv, "playlist.yml")

	_, body := do(t, srv, "POST", "/v1/recommenders/playlist_add/recommend", `{"items":["t1"]}`, bearer)
	var r recommend.Response
	_ = json.Unmarshal(body, &r)

	res, body := do(t, srv, "GET", "/v1/recommendations/"+r.RecID+"/explain/a", "", bearer)
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	var b recommend.Breakdown
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	if b.RecID != r.RecID || b.Item != "a" || b.Position != 1 || len(b.Breakdown) != 2 {
		t.Errorf("breakdown = %s", body)
	}

	res, body = do(t, srv, "GET", "/v1/recommendations/"+r.RecID+"/explain/zzz", "", bearer)
	wantError(t, res, body, 404, "unknown_item")
	res, body = do(t, srv, "GET", "/v1/recommendations/rec_nope/explain/a", "", bearer)
	wantError(t, res, body, 404, "unknown_recommendation")
}
