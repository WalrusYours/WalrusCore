package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timurcravtov/walrus/internal/rank"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store/memory"
)

const adminKey = "test-admin-key"

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	h := New(Config{
		AdminKey: adminKey, InstanceID: "abc123", InstanceName: "test", Version: "t",
		FailDelay: -1, MaxSchemaBytes: 64 << 10,
	}, realDeps())
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// realDeps is the engine as cmd/walrus wires it: the in-memory store and the real ranker.
func realDeps() Deps {
	st := memory.New()
	return Deps{Schema: schema.NewService(), Store: st, Ranker: rank.New(st)}
}

func example(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "schema", "schema-examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func do(t *testing.T, srv *httptest.Server, method, path, body string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

var bearer = map[string]string{"Authorization": "Bearer " + adminKey}

func decode(t *testing.T, b []byte) schema.Result {
	t.Helper()
	var r schema.Result
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return r
}

func TestHealthIsPublicAndNamesTheInstance(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/health", "/v1/health"} {
		res, body := do(t, srv, "GET", path, "", nil)
		var h map[string]string
		_ = json.Unmarshal(body, &h)
		if res.StatusCode != 200 || h["instance_id"] != "abc123" || h["instance_name"] != "test" || h["status"] != "ok" {
			t.Errorf("%s = %d %s", path, res.StatusCode, body)
		}
	}
}

func TestSchemaEndpointsNeedAuth(t *testing.T) {
	srv := newTestServer(t)
	for _, c := range []struct{ method, path string }{
		{"PUT", "/v1/schema"}, {"GET", "/v1/schema"}, {"GET", "/v1/schema/history"}, {"GET", "/v1/admin/session"},
	} {
		if res, _ := do(t, srv, c.method, c.path, "x", nil); res.StatusCode != 401 {
			t.Errorf("%s %s without credentials = %d, want 401", c.method, c.path, res.StatusCode)
		}
		if res, _ := do(t, srv, c.method, c.path, "x", map[string]string{"Authorization": "Bearer wrong"}); res.StatusCode != 401 {
			t.Errorf("%s %s with a wrong key = %d, want 401", c.method, c.path, res.StatusCode)
		}
	}
}

func TestLoginSetsAnHttpOnlyCookieThatAuthorises(t *testing.T) {
	srv := newTestServer(t)

	if res, _ := do(t, srv, "POST", "/v1/admin/session", `{"key":"nope"}`, nil); res.StatusCode != 401 {
		t.Fatalf("wrong key = %d, want 401", res.StatusCode)
	}
	if res, _ := do(t, srv, "POST", "/v1/admin/session", `not json`, nil); res.StatusCode != 400 {
		t.Fatalf("bad body = %d, want 400", res.StatusCode)
	}

	res, _ := do(t, srv, "POST", "/v1/admin/session", `{"key":"`+adminKey+`"}`, nil)
	if res.StatusCode != 204 {
		t.Fatalf("login = %d", res.StatusCode)
	}
	cookies := res.Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie = %+v", cookies)
	}
	if strings.Contains(cookies[0].Value, adminKey) {
		t.Fatal("the cookie must not contain the admin key")
	}

	cookie := map[string]string{"Cookie": cookies[0].Name + "=" + cookies[0].Value}
	if res, _ := do(t, srv, "GET", "/v1/admin/session", "", cookie); res.StatusCode != 204 {
		t.Fatalf("session status with cookie = %d", res.StatusCode)
	}
	if res, _ := do(t, srv, "GET", "/v1/schema/history", "", cookie); res.StatusCode != 200 {
		t.Fatalf("history with cookie = %d", res.StatusCode)
	}

	do(t, srv, "DELETE", "/v1/admin/session", "", cookie)
	if res, _ := do(t, srv, "GET", "/v1/admin/session", "", cookie); res.StatusCode != 401 {
		t.Fatalf("session after logout = %d, want 401", res.StatusCode)
	}
}

func TestPushFlow(t *testing.T) {
	srv := newTestServer(t)
	feed := example(t, "feed.yml")

	if res, _ := do(t, srv, "GET", "/v1/schema", "", bearer); res.StatusCode != 404 {
		t.Fatalf("GET before any push = %d, want 404", res.StatusCode)
	}

	res, body := do(t, srv, "PUT", "/v1/schema?dry_run=true", feed, bearer)
	if r := decode(t, body); res.StatusCode != 200 || !r.OK || r.Version != 0 {
		t.Fatalf("dry run = %d %s", res.StatusCode, body)
	}
	if res, _ := do(t, srv, "GET", "/v1/schema", "", bearer); res.StatusCode != 404 {
		t.Fatal("a dry run must not activate the schema")
	}

	res, body = do(t, srv, "PUT", "/v1/schema", feed, bearer)
	if r := decode(t, body); res.StatusCode != 200 || !r.OK || r.Version != 1 {
		t.Fatalf("apply = %d %s", res.StatusCode, body)
	}

	res, body = do(t, srv, "GET", "/v1/schema", "", bearer)
	var active struct {
		YAML    string `json:"yaml"`
		Version int    `json:"version"`
		Hash    string `json:"hash"`
	}
	_ = json.Unmarshal(body, &active)
	if res.StatusCode != 200 || active.Version != 1 || active.YAML != feed || len(active.Hash) != 64 {
		t.Fatalf("GET schema = %d %s", res.StatusCode, body)
	}

	// An additive edit applies as version 2.
	tweaked := strings.Replace(feed, "default: 0.3, on: created_at", "default: 0.7, on: created_at", 1)
	res, body = do(t, srv, "PUT", "/v1/schema", tweaked, bearer)
	if r := decode(t, body); res.StatusCode != 200 || r.Version != 2 || r.Diff.Verdict != schema.VerdictAdditive {
		t.Fatalf("additive = %d %s", res.StatusCode, body)
	}

	// A breaking edit is refused with 409 until confirmed.
	breaking := strings.Replace(tweaked, "type: low_exposure", "type: global_count", 1)
	res, body = do(t, srv, "PUT", "/v1/schema", breaking, bearer)
	if r := decode(t, body); res.StatusCode != 409 || !r.NeedsConfirm || r.Diff.Verdict != schema.VerdictBreaking {
		t.Fatalf("breaking = %d %s", res.StatusCode, body)
	}
	res, body = do(t, srv, "PUT", "/v1/schema?confirm_breaking=true", breaking, bearer)
	if r := decode(t, body); res.StatusCode != 200 || r.Version != 3 {
		t.Fatalf("confirmed = %d %s", res.StatusCode, body)
	}

	res, body = do(t, srv, "GET", "/v1/schema/history", "", bearer)
	var hist []schema.Version
	_ = json.Unmarshal(body, &hist)
	if res.StatusCode != 200 || len(hist) != 3 || hist[0].Version != 3 || hist[2].Version != 1 {
		t.Fatalf("history = %d %s", res.StatusCode, body)
	}
}

func TestInvalidSchemaReturns400WithPaths(t *testing.T) {
	srv := newTestServer(t)
	bad := strings.Replace(example(t, "feed.yml"), "type: item_neighbors", "type: magic", 1)
	res, body := do(t, srv, "PUT", "/v1/schema", bad, bearer)
	r := decode(t, body)
	if res.StatusCode != 400 || r.OK || len(r.Errors) == 0 || r.Errors[0].Path != "signals.content.type" {
		t.Fatalf("invalid = %d %s", res.StatusCode, body)
	}

	res, body = do(t, srv, "PUT", "/v1/schema", "version: [", bearer)
	if r := decode(t, body); res.StatusCode != 400 || len(r.Errors) != 1 {
		t.Fatalf("unparseable = %d %s", res.StatusCode, body)
	}
}

func TestOversizedSchemaIsRejected(t *testing.T) {
	srv := newTestServer(t)
	res, _ := do(t, srv, "PUT", "/v1/schema", strings.Repeat("# padding\n", 10000), bearer)
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized = %d, want 413", res.StatusCode)
	}
}

func TestEveryExampleCanBePushedAsItsOwnTenantSchema(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "docs", "schema", "schema-examples", "*.yml"))
	for _, f := range files {
		srv := newTestServer(t)
		res, body := do(t, srv, "PUT", "/v1/schema", example(t, filepath.Base(f)), bearer)
		if r := decode(t, body); res.StatusCode != 200 || !r.OK {
			t.Errorf("%s = %d %s", filepath.Base(f), res.StatusCode, body)
		}
	}
}

func TestASchemaPushWarnsAboutWhatIsNotBuiltYet(t *testing.T) {
	srv := newTestServer(t)
	res, body := do(t, srv, "PUT", "/v1/schema", example(t, "spotify.yml"), bearer)
	r := decode(t, body)
	if res.StatusCode != 200 || !r.OK {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	found := false
	for _, w := range r.Warnings {
		if w.Path == "signals.trending.type" && strings.Contains(w.Message, "not ranked yet") {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning for the trend signal: %+v", r.Warnings)
	}

	res, body = do(t, srv, "PUT", "/v1/schema?confirm_breaking=true", example(t, "playlist.yml"), bearer)
	if r := decode(t, body); res.StatusCode != 200 || len(r.Warnings) != 0 {
		t.Errorf("playlist.yml uses only built features: %d %+v", res.StatusCode, r.Warnings)
	}
}
