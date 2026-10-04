package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store"
	"github.com/timurcravtov/walrus/internal/store/memory"
)

func setup(t *testing.T) (*Service, store.Store) {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("..", "..", "docs", "schema", "schema-examples", "spotify.yml"))
	if err != nil {
		t.Fatal(err)
	}
	sch := schema.NewService()
	if res := sch.Load(text, schema.LoadOptions{Author: "test"}); !res.OK {
		t.Fatalf("schema rejected: %+v", res.Errors)
	}
	st := memory.New()
	return NewService(sch, st), st
}

func track(id string, override map[string]any) Raw {
	attrs := map[string]any{
		"artist_id": "acdc", "album_id": "alb_1", "genres": []any{"rock", "hard rock"}, "language": "en",
		"explicit": false, "release_date": "1980-07-25T00:00:00Z", "duration_ms": 255000.0,
		"available_in": []any{"MD", "US"}, "energy": 0.92, "valence": 0.65,
		"danceability": 0.5, "acousticness": 0.05, "tempo": 128.0,
	}
	for k, v := range override {
		if v == nil {
			delete(attrs, k)
		} else {
			attrs[k] = v
		}
	}
	return Raw{Entity: "track", ID: id, Attributes: attrs}
}

func TestStoresValidEntitiesAsTypedValues(t *testing.T) {
	s, st := setup(t)
	res, err := s.Entities(context.Background(), []Raw{
		track("back_in_black", nil),
		{Entity: "artist", ID: "acdc", Attributes: map[string]any{"genres": []any{"rock"}}},
	})
	if err != nil || res.Accepted != 2 || len(res.Rejected) != 0 {
		t.Fatalf("%+v %v", res, err)
	}

	got, err := st.Entity(context.Background(), "track", "back_in_black")
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := got.Attrs["energy"].AsFloat(); f != 0.92 {
		t.Errorf("energy = %v", got.Attrs["energy"])
	}
	if !got.Attrs["genres"].Equal(domain.Set("hard rock", "rock")) {
		t.Errorf("genres = %v", got.Attrs["genres"])
	}
	if ts, ok := got.Attrs["release_date"].AsTime(); !ok || ts.Year() != 1980 {
		t.Errorf("release_date = %v", got.Attrs["release_date"])
	}
	if b, ok := got.Attrs["explicit"].AsBool(); !ok || b {
		t.Errorf("explicit = %v", got.Attrs["explicit"])
	}
	if _, has := got.Attrs["audio_embedding"]; has {
		t.Error("an optional attribute that was not sent should be absent")
	}
}

func TestEachKindOfBadEntityIsRejectedWithItsReason(t *testing.T) {
	s, _ := setup(t)
	vec := make([]any, 3)
	for _, c := range []struct {
		name string
		raw  Raw
		want string
	}{
		{"unknown type", Raw{Entity: "song", ID: "x"}, `unknown entity type "song"`},
		{"no id", track("", nil), "id must be"},
		{"long id", track(strings.Repeat("x", 201), nil), "id must be"},
		{"unknown attribute", track("x", map[string]any{"mood": "sad"}), `has no attribute "mood"`},
		{"missing attribute", track("x", map[string]any{"energy": nil}), `missing attribute "energy"`},
		{"string for a number", track("x", map[string]any{"tempo": "fast"}), `attribute "tempo": expected a number, got a string`},
		{"fraction for an int", track("x", map[string]any{"duration_ms": 1.5}), "expected a whole number"},
		{"outside the range", track("x", map[string]any{"energy": 1.5}), "outside the range [0, 1]"},
		{"bad timestamp", track("x", map[string]any{"release_date": "1980"}), "not an RFC 3339 timestamp"},
		{"number for a bool", track("x", map[string]any{"explicit": 1.0}), "expected true or false"},
		{"string for a set", track("x", map[string]any{"genres": "rock"}), "expected a list of strings"},
		{"number in a set", track("x", map[string]any{"genres": []any{"rock", 3.0}}), "item 1: expected a string"},
		{"wrong vector size", track("x", map[string]any{"audio_embedding": vec}), "expected 512 numbers, got 3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, err := s.Entities(context.Background(), []Raw{c.raw})
			if err != nil {
				t.Fatal(err)
			}
			if res.Accepted != 0 || len(res.Rejected) != 1 || !strings.Contains(res.Rejected[0].Error, c.want) {
				t.Errorf("got %+v, want a rejection containing %q", res, c.want)
			}
		})
	}
}

func TestABadEntityDoesNotStopTheRestOfTheBatch(t *testing.T) {
	s, st := setup(t)
	res, err := s.Entities(context.Background(), []Raw{
		track("a", nil),
		track("b", map[string]any{"energy": 7.0}),
		track("c", nil),
	})
	if err != nil || res.Accepted != 2 || len(res.Rejected) != 1 || res.Rejected[0].Index != 1 || res.Rejected[0].ID != "b" {
		t.Fatalf("%+v %v", res, err)
	}
	if n, _ := st.CountEntities(context.Background()); n["track"] != 2 {
		t.Errorf("counts = %v", n)
	}
	if _, err := st.Entity(context.Background(), "track", "b"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the rejected entity was stored: %v", err)
	}
}

func TestSendingTheSameEntityAgainUpdatesIt(t *testing.T) {
	s, st := setup(t)
	s.Entities(context.Background(), []Raw{track("a", map[string]any{"energy": 0.1})})
	s.Entities(context.Background(), []Raw{track("a", map[string]any{"energy": 0.9})})
	got, _ := st.Entity(context.Background(), "track", "a")
	if f, _ := got.Attrs["energy"].AsFloat(); f != 0.9 {
		t.Errorf("energy = %v", f)
	}
	if n, _ := st.CountEntities(context.Background()); n["track"] != 1 {
		t.Errorf("counts = %v", n)
	}
}

func TestBatchLevelErrors(t *testing.T) {
	s, _ := setup(t)
	check := func(raws []Raw, status int, code string) {
		t.Helper()
		_, err := s.Entities(context.Background(), raws)
		var e *Error
		if !errors.As(err, &e) || e.Status != status || e.Code != code {
			t.Errorf("got %v, want %d %s", err, status, code)
		}
	}
	check(nil, 400, "validation_error")
	check(make([]Raw, MaxBatch+1), 400, "validation_error")

	noSchema := NewService(schema.NewService(), memory.New())
	_, err := noSchema.Entities(context.Background(), []Raw{track("a", nil)})
	var e *Error
	if !errors.As(err, &e) || e.Status != 409 || e.Code != "schema_missing" {
		t.Errorf("before a schema: %v", err)
	}
}

func TestRawEntityDecodesFromTheJSONTheAPIReceives(t *testing.T) {
	var r Raw
	body := `{"entity":"track","id":"a","attributes":{"energy":0.5,"genres":["rock"],"explicit":false}}`
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	if r.Entity != "track" || r.ID != "a" || r.Attributes["energy"] != 0.5 {
		t.Errorf("%+v", r)
	}
	if _, ok := r.Attributes["genres"].([]any); !ok {
		t.Error("lists must arrive as []any, which is what value() reads")
	}
}
