package api

import (
	"encoding/json"
	"fmt"
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
