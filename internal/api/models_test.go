package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/learn"
	"github.com/timurcravtov/walrus/internal/rank"
	"github.com/timurcravtov/walrus/internal/recommend"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store"
	"github.com/timurcravtov/walrus/internal/store/memory"
)

const reelSchema = `version: 1
meta: { name: Reel, locales: [en], default_locale: en }
entities:
  user: { key: id }
  film: { key: id, attributes: { title: { type: string } } }
interactions:
  watch: { kind: positive, weight: 2, half_life: 60d }
signals:
  taste:      { type: embedding, for: film, factors: 2, regularization: 0.1, alpha: 10, iterations: 15, default: 0.6 }
  popularity: { type: global_count, normalise: rank, default: 0.1 }
recommenders:
  home:
    for: [film]
    seed: user
    candidates:
      - { source: factors, signal: taste, cap: 50 }
      - { source: popular, cap: 50 }
    signals: [taste, popularity]
recommendable: film
`

type modelsView struct {
	Models []learn.State `json:"models"`
	Job    learn.Job     `json:"job"`
}

func getModels(t *testing.T, srv *httptest.Server) modelsView {
	t.Helper()
	res, raw := do(t, srv, "GET", "/v1/models", "", bearer)
	if res.StatusCode != 200 {
		t.Fatalf("GET /v1/models = %d %s", res.StatusCode, raw)
	}
	var v modelsView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

// waitForTraining waits until the run that was started has finished.
func waitForTraining(t *testing.T, srv *httptest.Server) modelsView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v := getModels(t, srv); !v.Job.Running && v.Job.FinishedAt != nil {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatal("training did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func push(t *testing.T, srv *httptest.Server, text string) {
	t.Helper()
	if res, raw := do(t, srv, "PUT", "/v1/schema", text, bearer); res.StatusCode != 200 {
		t.Fatalf("PUT /v1/schema = %d %s", res.StatusCode, raw)
	}
}

// sendTwoCrowds sends 12 films and 40 viewers: 20 watch four of films 0-5, 20 four of 6-11.
func sendTwoCrowds(t *testing.T, srv *httptest.Server) {
	t.Helper()
	var films, events []string
	for i := 0; i < 12; i++ {
		films = append(films, fmt.Sprintf(`{"entity":"film","id":"f%02d","attributes":{"title":"Film %02d"}}`, i, i))
	}
	rng := rand.New(rand.NewPCG(3, 8))
	now := time.Now().UTC()
	for u := 0; u < 40; u++ {
		first := 0
		if u >= 20 {
			first = 6
		}
		for k, pick := range rng.Perm(6)[:4] {
			events = append(events, fmt.Sprintf(`{"user":"viewer%02d","type":"watch","target":"f%02d","ts":%q}`,
				u, first+pick, now.Add(-time.Duration(u+k)*time.Hour).Format(time.RFC3339)))
		}
	}
	if res, raw := do(t, srv, "POST", "/v1/entities", `{"entities":[`+strings.Join(films, ",")+`]}`, bearer); res.StatusCode != 200 || !strings.Contains(string(raw), `"rejected":[]`) {
		t.Fatalf("films: %d %s", res.StatusCode, raw)
	}
	if res, raw := do(t, srv, "POST", "/v1/interactions", `{"interactions":[`+strings.Join(events, ",")+`]}`, bearer); res.StatusCode != 200 || !strings.Contains(string(raw), `"rejected":[]`) {
		t.Fatalf("interactions: %d %s", res.StatusCode, raw)
	}
}

func TestModelEndpointsNeedTheAdminKey(t *testing.T) {
	srv := newTestServer(t)
	for _, c := range []struct{ method, path string }{{"GET", "/v1/models"}, {"POST", "/v1/models/train"}} {
		if res, _ := do(t, srv, c.method, c.path, "", nil); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a key = %d, want 401", c.method, c.path, res.StatusCode)
		}
		if res, _ := do(t, srv, c.method, c.path, "", map[string]string{"Authorization": "Bearer wrong"}); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s with a wrong key = %d, want 401", c.method, c.path, res.StatusCode)
		}
	}
}

func TestModelEndpointsBeforeAnySchema(t *testing.T) {
	srv := newTestServer(t)
	for _, c := range []struct{ method, path string }{{"GET", "/v1/models"}, {"POST", "/v1/models/train"}} {
		res, raw := do(t, srv, c.method, c.path, "", bearer)
		if res.StatusCode != http.StatusNotFound || !strings.Contains(string(raw), "no_schema") {
			t.Errorf("%s %s = %d %s, want 404 no_schema", c.method, c.path, res.StatusCode, raw)
		}
	}
}

func TestTrainOverHTTPThenRecommend(t *testing.T) {
	srv := newTestServer(t)
	push(t, srv, reelSchema)
	sendTwoCrowds(t, srv)

	v := getModels(t, srv)
	if len(v.Models) != 1 {
		t.Fatalf("models = %+v", v.Models)
	}
	if m := v.Models[0]; m.Signal != "taste" || m.Entity != "film" || m.Trained || !m.RetrainPending || m.Reason != "not trained yet" || m.Wanted.Factors != 2 {
		t.Errorf("before training: %+v", m)
	}
	if v.Job.Running || v.Job.StartedAt != nil {
		t.Errorf("no run yet: %+v", v.Job)
	}

	res, raw := do(t, srv, "POST", "/v1/models/train", "", bearer)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /v1/models/train = %d %s, want 202", res.StatusCode, raw)
	}
	v = waitForTraining(t, srv)
	if v.Job.Trigger != "api" || v.Job.Error != "" || len(v.Job.Results) != 1 || v.Job.Results[0].Version != 1 || v.Job.Results[0].Error != "" {
		t.Errorf("job = %+v", v.Job)
	}
	m := v.Models[0]
	if !m.Trained || m.RetrainPending || m.Version != 1 || m.Items != 12 || m.Users != 40 || m.Interactions != 160 || m.TrainedAt == nil || m.Settings.Factors != 2 {
		t.Errorf("after training: %+v", m)
	}

	// a viewer the model never saw, with two second-crowd films, is recommended the rest of that crowd
	now := time.Now().UTC().Format(time.RFC3339)
	body := fmt.Sprintf(`{"interactions":[{"user":"late","type":"watch","target":"f07","ts":%q},{"user":"late","type":"watch","target":"f09","ts":%q}]}`, now, now)
	if res, raw := do(t, srv, "POST", "/v1/interactions", body, bearer); res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, raw)
	}
	res, raw = do(t, srv, "POST", "/v1/recommenders/home/recommend", `{"user":"late","limit":4,"explain":true}`, bearer)
	if res.StatusCode != 200 {
		t.Fatalf("recommend = %d %s", res.StatusCode, raw)
	}
	var rec recommend.Response
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.Items) == 0 {
		t.Fatal("no recommendations")
	}
	for _, it := range rec.Items[:min(3, len(rec.Items))] {
		if it.ID < "f06" {
			t.Errorf("a second-crowd viewer was shown %s first: %+v", it.ID, rec.Items)
		}
	}
	if !strings.Contains(rec.Items[0].Reason, "Because you interacted with") {
		t.Errorf("reason = %q", rec.Items[0].Reason)
	}
}

func TestChangingTheSchemaMarksTheModelStaleButKeepsServing(t *testing.T) {
	srv := newTestServer(t)
	push(t, srv, reelSchema)
	sendTwoCrowds(t, srv)
	do(t, srv, "POST", "/v1/models/train", "", bearer)
	waitForTraining(t, srv)

	// a weight change is T3: the model is still current
	push(t, srv, strings.Replace(reelSchema, "default: 0.6", "default: 0.9", 1))
	if m := getModels(t, srv).Models[0]; m.RetrainPending || !m.Trained {
		t.Errorf("a weight change must not ask for a retrain: %+v", m)
	}

	// a factor count is T1: the model is stale, and the old one keeps serving until a run replaces it
	push(t, srv, strings.Replace(reelSchema, "factors: 2", "factors: 3", 1))
	m := getModels(t, srv).Models[0]
	if !m.RetrainPending || !m.Trained || m.Version != 1 || m.Settings.Factors != 2 || m.Wanted.Factors != 3 || !strings.Contains(m.Reason, "settings changed") {
		t.Errorf("after a T1 change: %+v", m)
	}
	body := fmt.Sprintf(`{"interactions":[{"user":"late","type":"watch","target":"f07","ts":%q}]}`, time.Now().UTC().Format(time.RFC3339))
	do(t, srv, "POST", "/v1/interactions", body, bearer)
	if res, raw := do(t, srv, "POST", "/v1/recommenders/home/recommend", `{"user":"late","limit":4}`, bearer); res.StatusCode != 200 {
		t.Errorf("a stale model must keep serving: %d %s", res.StatusCode, raw)
	}

	do(t, srv, "POST", "/v1/models/train", "", bearer)
	m = waitForTraining(t, srv).Models[0]
	if m.RetrainPending || m.Version != 2 || m.Settings.Factors != 3 {
		t.Errorf("after retraining: %+v", m)
	}
}

func TestTrainErrors(t *testing.T) {
	srv := newTestServer(t)
	push(t, srv, reelSchema)
	for _, c := range []struct {
		name, path string
		status     int
		code       string
	}{
		{"an unknown signal", "/v1/models/train?signal=nope", http.StatusNotFound, "unknown_signal"},
		{"a signal that is not an embedding", "/v1/models/train?signal=popularity", http.StatusNotFound, "unknown_signal"},
	} {
		res, raw := do(t, srv, "POST", c.path, "", bearer)
		if res.StatusCode != c.status || !strings.Contains(string(raw), c.code) {
			t.Errorf("%s: %d %s, want %d %s", c.name, res.StatusCode, raw, c.status, c.code)
		}
	}

	// a schema with no embedding signal has nothing to train
	other := newTestServer(t)
	push(t, other, example(t, "spotify.yml"))
	res, raw := do(t, other, "POST", "/v1/models/train", "", bearer)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "nothing_to_train") {
		t.Errorf("no embedding signal: %d %s", res.StatusCode, raw)
	}
	if v := getModels(t, other); len(v.Models) != 0 {
		t.Errorf("models = %+v, want an empty list (not null)", v.Models)
	}
	if _, raw := do(t, other, "GET", "/v1/models", "", bearer); !strings.Contains(string(raw), `"models":[]`) {
		t.Errorf("the list must encode as [], got %s", raw)
	}
}

// gateStore holds a training run inside the store until it is released.
type gateStore struct {
	store.Store
	started, release chan struct{}
	once             sync.Once
}

func (g *gateStore) ListEntities(ctx context.Context, typ string) ([]domain.Entity, error) {
	g.once.Do(func() { close(g.started) })
	<-g.release
	return g.Store.ListEntities(ctx, typ)
}

func TestASecondTrainWhileOneRunsIsRefused(t *testing.T) {
	g := &gateStore{Store: memory.New(), started: make(chan struct{}), release: make(chan struct{})}
	srv := httptest.NewServer(New(Config{AdminKey: adminKey, InstanceID: "x", FailDelay: -1}, Deps{
		Schema: schema.NewService(), Store: g, Ranker: rank.New(g),
	}))
	t.Cleanup(srv.Close)
	push(t, srv, reelSchema)
	sendTwoCrowds(t, srv)

	if res, raw := do(t, srv, "POST", "/v1/models/train", "", bearer); res.StatusCode != http.StatusAccepted {
		t.Fatalf("first start = %d %s", res.StatusCode, raw)
	}
	<-g.started
	if v := getModels(t, srv); !v.Job.Running {
		t.Errorf("a run in progress must show as running: %+v", v.Job)
	}
	res, raw := do(t, srv, "POST", "/v1/models/train", "", bearer)
	if res.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "already_running") {
		t.Errorf("second start = %d %s, want 409 already_running", res.StatusCode, raw)
	}
	close(g.release)
	if v := waitForTraining(t, srv); v.Job.Results[0].Version != 1 {
		t.Errorf("after the run: %+v", v.Job)
	}
}
