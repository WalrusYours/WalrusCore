package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

const trackJSON = `{"entity":"track","id":"%s","attributes":{"artist_id":"acdc","album_id":"a1","genres":["rock"],"language":"en",
"explicit":false,"release_date":"1980-07-25T00:00:00Z","duration_ms":255000,"available_in":["MD"],
"energy":%s,"valence":0.6,"danceability":0.5,"acousticness":0.1,"tempo":120}}`

func track(id, energy string) string {
	return strings.Replace(strings.Replace(trackJSON, "%s", id, 1), "%s", energy, 1)
}

func TestEntitiesNeedAuth(t *testing.T) {
	srv, _ := newRecommendServer(t)
	for _, c := range []struct{ method, path string }{
		{"POST", "/v1/entities"}, {"GET", "/v1/entities"}, {"GET", "/v1/entities/track/a"},
	} {
		if res, _ := do(t, srv, c.method, c.path, "{}", nil); res.StatusCode != 401 {
			t.Errorf("%s %s without a key = %d", c.method, c.path, res.StatusCode)
		}
	}
}

func TestEntitiesBeforeAnySchemaIs409(t *testing.T) {
	srv, _ := newRecommendServer(t)
	res, body := do(t, srv, "POST", "/v1/entities", `{"entities":[`+track("a", "0.5")+`]}`, bearer)
	wantError(t, res, body, 409, "schema_missing")
}

func TestSendAndReadEntities(t *testing.T) {
	srv, _ := newRecommendServer(t)
	pushExample(t, srv, "spotify.yml")

	body := `{"entities":[` + track("a", "0.9") + `,` + track("b", "7") + `,` + track("c", "0.2") + `]}`
	res, raw := do(t, srv, "POST", "/v1/entities", body, bearer)
	var out struct {
		Accepted int
		Rejected []struct {
			Index int
			ID    string
			Error string
		}
	}
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode != 200 || out.Accepted != 2 || len(out.Rejected) != 1 || out.Rejected[0].ID != "b" ||
		!strings.Contains(out.Rejected[0].Error, "outside the range") {
		t.Fatalf("%d %s", res.StatusCode, raw)
	}

	res, raw = do(t, srv, "GET", "/v1/entities", "", bearer)
	if res.StatusCode != 200 || !strings.Contains(string(raw), `"track":2`) {
		t.Errorf("counts: %d %s", res.StatusCode, raw)
	}

	res, raw = do(t, srv, "GET", "/v1/entities/track/a", "", bearer)
	var e struct {
		Entity, ID string
		Attributes map[string]any
	}
	_ = json.Unmarshal(raw, &e)
	if res.StatusCode != 200 || e.Entity != "track" || e.ID != "a" || e.Attributes["energy"] != 0.9 {
		t.Errorf("get: %d %s", res.StatusCode, raw)
	}

	res, raw = do(t, srv, "GET", "/v1/entities/track/b", "", bearer)
	wantError(t, res, raw, 404, "unknown_entity")
}

func TestEntityBatchErrors(t *testing.T) {
	srv, _ := newRecommendServer(t)
	pushExample(t, srv, "spotify.yml")
	for name, body := range map[string]string{
		"empty":         `{"entities":[]}`,
		"no body":       ``,
		"unknown field": `{"items":[]}`,
		"not json":      `{`,
	} {
		res, raw := do(t, srv, "POST", "/v1/entities", body, bearer)
		if res.StatusCode != 400 {
			t.Errorf("%s: %d %s", name, res.StatusCode, raw)
		}
	}
}

const trackAttrs = `{"attributes":{"artist_id":"acdc","album_id":"a1","genres":["rock"],"language":"en",
"explicit":false,"release_date":"1980-07-25T00:00:00Z","duration_ms":255000,"available_in":["MD"],
"energy":%s,"valence":0.6,"danceability":0.5,"acousticness":0.1,"tempo":120}}`

func TestPutOneEntity(t *testing.T) {
	srv, _ := newRecommendServer(t)
	body := func(energy string) string { return strings.Replace(trackAttrs, "%s", energy, 1) }

	res, raw := do(t, srv, "PUT", "/v1/entities/track/a", body("0.5"), bearer)
	wantError(t, res, raw, 409, "schema_missing")

	pushExample(t, srv, "spotify.yml")
	if res, raw = do(t, srv, "PUT", "/v1/entities/track/a", body("0.5"), bearer); res.StatusCode != 201 {
		t.Fatalf("create: %d %s", res.StatusCode, raw)
	}
	res, raw = do(t, srv, "PUT", "/v1/entities/track/a", body("0.9"), bearer)
	if res.StatusCode != 200 || !strings.Contains(string(raw), `"energy":0.9`) {
		t.Fatalf("replace: %d %s", res.StatusCode, raw)
	}
	if _, raw = do(t, srv, "GET", "/v1/entities", "", bearer); !strings.Contains(string(raw), `"track":1`) {
		t.Errorf("counts: %s", raw)
	}

	res, raw = do(t, srv, "PUT", "/v1/entities/track/b", body("7"), bearer)
	e := wantError(t, res, raw, 400, "validation_error")
	if !strings.Contains(e.Error.Message, "outside the range") {
		t.Errorf("message = %q", e.Error.Message)
	}
	res, raw = do(t, srv, "PUT", "/v1/entities/song/b", body("0.5"), bearer)
	wantError(t, res, raw, 400, "validation_error")
	if res, _ = do(t, srv, "PUT", "/v1/entities/track/a", body("0.5"), nil); res.StatusCode != 401 {
		t.Errorf("without a key = %d", res.StatusCode)
	}
}

func TestImportJSONLines(t *testing.T) {
	srv, _ := newRecommendServer(t)
	line := func(id, energy string) string { return strings.ReplaceAll(track(id, energy), "\n", "") }

	res, raw := do(t, srv, "POST", "/v1/import", line("a", "0.5"), bearer)
	wantError(t, res, raw, 409, "schema_missing")

	pushExample(t, srv, "spotify.yml")
	body := strings.Join([]string{
		line("a", "0.1"),
		"",
		line("b", "9"),
		"{not json",
		line("c", "0.3"),
		`{"entity":"track","id":"d","attributes":{},"extra":1}`,
	}, "\n")
	res, raw = do(t, srv, "POST", "/v1/import", body, bearer)
	var out struct {
		Lines, Accepted, Rejected int
		Reports                   []struct {
			Line int
			ID   string
		}
	}
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode != 200 || out.Lines != 5 || out.Accepted != 2 || out.Rejected != 3 || len(out.Reports) != 3 {
		t.Fatalf("%d %s", res.StatusCode, raw)
	}
	if out.Reports[0].Line != 3 || out.Reports[0].ID != "b" || out.Reports[1].Line != 4 || out.Reports[2].Line != 6 {
		t.Errorf("reports should carry the line numbers of the body: %s", raw)
	}

	// running the same import again changes nothing
	do(t, srv, "POST", "/v1/import", body, bearer)
	if _, raw = do(t, srv, "GET", "/v1/entities", "", bearer); !strings.Contains(string(raw), `"track":2`) {
		t.Errorf("counts after a repeat: %s", raw)
	}
}

func TestImportRunsInChunksAndCapsItsReports(t *testing.T) {
	srv, _ := newRecommendServer(t)
	pushExample(t, srv, "spotify.yml")
	var lines []string
	for i := 0; i < 1200; i++ {
		energy := "0.5"
		if i%4 == 0 {
			energy = "9"
		}
		lines = append(lines, strings.ReplaceAll(track(fmt.Sprintf("t%d", i), energy), "\n", ""))
	}
	res, raw := do(t, srv, "POST", "/v1/import", strings.Join(lines, "\n"), bearer)
	var out struct {
		Lines, Accepted, Rejected int
		Reports                   []any
	}
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode != 200 || out.Lines != 1200 || out.Accepted != 900 || out.Rejected != 300 || len(out.Reports) != 100 {
		t.Fatalf("%d lines=%d accepted=%d rejected=%d reports=%d", res.StatusCode, out.Lines, out.Accepted, out.Rejected, len(out.Reports))
	}
	if _, raw = do(t, srv, "GET", "/v1/entities", "", bearer); !strings.Contains(string(raw), `"track":900`) {
		t.Errorf("counts: %s", raw)
	}
}

func TestSendInteractions(t *testing.T) {
	srv, _ := newRecommendServer(t)
	body := `{"interactions":[
{"user":"u1","type":"add_to_playlist","target":"a","ts":"2026-09-01T10:00:00Z","fields":{"playlist_id":"p1"}},
{"user":"u1","type":"dance","target":"a"}]}`

	res, raw := do(t, srv, "POST", "/v1/interactions", body, bearer)
	wantError(t, res, raw, 409, "schema_missing")

	pushExample(t, srv, "spotify.yml")
	res, raw = do(t, srv, "POST", "/v1/interactions", body, bearer)
	if res.StatusCode != 200 || !strings.Contains(string(raw), `"accepted":1`) || !strings.Contains(string(raw), "unknown interaction type") {
		t.Fatalf("%d %s", res.StatusCode, raw)
	}
	if _, raw = do(t, srv, "GET", "/v1/interactions", "", bearer); !strings.Contains(string(raw), `"add_to_playlist":1`) {
		t.Errorf("counts: %s", raw)
	}
	if res, _ = do(t, srv, "POST", "/v1/interactions", body, nil); res.StatusCode != 401 {
		t.Errorf("without a key = %d", res.StatusCode)
	}
	if res, _ = do(t, srv, "POST", "/v1/interactions", `{"interactions":[]}`, bearer); res.StatusCode != 400 {
		t.Errorf("empty = %d", res.StatusCode)
	}
}

func TestSchemaKnobsDescribeThePanel(t *testing.T) {
	srv, _ := newRecommendServer(t)
	res, raw := do(t, srv, "GET", "/v1/schema/knobs", "", bearer)
	wantError(t, res, raw, 409, "schema_missing")
	if res, _ = do(t, srv, "GET", "/v1/schema/knobs", "", nil); res.StatusCode != 401 {
		t.Errorf("without a key = %d", res.StatusCode)
	}

	pushExample(t, srv, "spotify.yml")
	res, raw = do(t, srv, "GET", "/v1/schema/knobs", "", bearer)
	var out struct {
		Knobs []struct {
			ID, Kind, Label, Group string
			Min, Max, Default      float64
			Recommenders           []string
		}
		Presets []struct {
			ID    string
			Knobs map[string]float64
		}
	}
	if err := json.Unmarshal(raw, &out); err != nil || res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, raw)
	}
	byID := map[string]int{}
	for i, k := range out.Knobs {
		byID[k.ID] = i
	}
	vibe := out.Knobs[byID["vibe_vs_branch_out"]]
	if vibe.Kind != "slider" || vibe.Group != "Playlist suggestions" || vibe.Default != 0.3 || vibe.Min != 0 || vibe.Max != 1 ||
		len(vibe.Recommenders) != 1 || vibe.Recommenders[0] != "playlist_add" {
		t.Errorf("vibe_vs_branch_out = %+v", vibe)
	}
	if mix := out.Knobs[byID["mix_in_my_taste"]]; mix.Kind != "toggle" || mix.Default != 1 {
		t.Errorf("mix_in_my_taste = %+v", mix)
	}
	// a feed knob is offered by the feed recommender, not by playlist_add
	if taste := out.Knobs[byID["taste_vs_crowd"]]; slices.Contains(taste.Recommenders, "playlist_add") || !slices.Contains(taste.Recommenders, "home") {
		t.Errorf("taste_vs_crowd offered by %v", taste.Recommenders)
	}
	// defaults include the default preset (taste_vs_crowd is 0.5 there)
	if out.Knobs[byID["taste_vs_crowd"]].Default != 0.5 || len(out.Presets) == 0 {
		t.Errorf("presets = %+v", out.Presets)
	}
}
