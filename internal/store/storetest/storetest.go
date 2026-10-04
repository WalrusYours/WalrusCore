package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/factors"
	"github.com/timurcravtov/walrus/internal/store"
)

func track(id string, energy float64, genres ...string) domain.Entity {
	return domain.Entity{Type: "track", ID: domain.EntityID(id), Attrs: map[string]domain.Value{
		"energy": domain.Num(energy), "genres": domain.Set(genres...), "title": domain.Str(id),
	}}
}

func Run(t *testing.T, newStore func() store.Store) {
	ctx := context.Background()

	t.Run("an entity can be stored and read back", func(t *testing.T) {
		s := newStore()
		if err := s.UpsertEntities(ctx, []domain.Entity{track("a", 0.9, "rock", "metal")}); err != nil {
			t.Fatal(err)
		}
		got, err := s.Entity(ctx, "track", "a")
		if err != nil {
			t.Fatal(err)
		}
		if got.Type != "track" || got.ID != "a" || !got.Attrs["energy"].Equal(domain.Num(0.9)) ||
			!got.Attrs["genres"].Equal(domain.Set("metal", "rock")) {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("a missing entity is ErrNotFound, also under another type", func(t *testing.T) {
		s := newStore()
		_ = s.UpsertEntities(ctx, []domain.Entity{track("a", 0.5)})
		for _, c := range [][2]string{{"track", "zzz"}, {"artist", "a"}} {
			if _, err := s.Entity(ctx, c[0], domain.EntityID(c[1])); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("Entity(%v) = %v, want ErrNotFound", c, err)
			}
		}
	})

	t.Run("upsert replaces and does not duplicate", func(t *testing.T) {
		s := newStore()
		_ = s.UpsertEntities(ctx, []domain.Entity{track("a", 0.1)})
		_ = s.UpsertEntities(ctx, []domain.Entity{track("a", 0.9)})
		got, _ := s.Entity(ctx, "track", "a")
		if !got.Attrs["energy"].Equal(domain.Num(0.9)) {
			t.Errorf("energy = %v, want the second write", got.Attrs["energy"])
		}
		if n, _ := s.CountEntities(ctx); n["track"] != 1 {
			t.Errorf("counts = %v", n)
		}
	})

	t.Run("the same id under two types is two entities", func(t *testing.T) {
		s := newStore()
		_ = s.UpsertEntities(ctx, []domain.Entity{
			track("x", 0.5),
			{Type: "artist", ID: "x", Attrs: map[string]domain.Value{"genres": domain.Set("rock")}},
		})
		n, _ := s.CountEntities(ctx)
		if n["track"] != 1 || n["artist"] != 1 || len(n) != 2 {
			t.Errorf("counts = %v", n)
		}
	})

	t.Run("a bad entity stores nothing", func(t *testing.T) {
		s := newStore()
		err := s.UpsertEntities(ctx, []domain.Entity{track("a", 0.5), {Type: "track"}})
		if err == nil {
			t.Fatal("an entity without an id should be refused")
		}
		if n, _ := s.CountEntities(ctx); len(n) != 0 {
			t.Errorf("a failed batch left %v behind", n)
		}
	})

	t.Run("Entities keeps the order asked and skips unknown ids", func(t *testing.T) {
		s := newStore()
		_ = s.UpsertEntities(ctx, []domain.Entity{track("a", 0.1), track("b", 0.2), track("c", 0.3)})
		got, err := s.Entities(ctx, "track", []domain.EntityID{"c", "nope", "a"})
		if err != nil || len(got) != 2 || got[0].ID != "c" || got[1].ID != "a" {
			t.Errorf("got %+v, %v", got, err)
		}
		if got, _ := s.Entities(ctx, "track", nil); len(got) != 0 {
			t.Errorf("no ids should give nothing: %+v", got)
		}
	})

	t.Run("ListEntities is sorted by id and only holds that type", func(t *testing.T) {
		s := newStore()
		_ = s.UpsertEntities(ctx, []domain.Entity{
			track("b", 0), track("c", 0), track("a", 0),
			{Type: "artist", ID: "z", Attrs: map[string]domain.Value{}},
		})
		got, _ := s.ListEntities(ctx, "track")
		if len(got) != 3 || got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
			t.Errorf("got %+v", got)
		}
		if got, _ := s.ListEntities(ctx, "nothing"); len(got) != 0 {
			t.Errorf("an unknown type is empty: %+v", got)
		}
	})

	t.Run("callers cannot change stored data", func(t *testing.T) {
		s := newStore()
		in := track("a", 0.5)
		_ = s.UpsertEntities(ctx, []domain.Entity{in})
		in.Attrs["energy"] = domain.Num(1)

		got, _ := s.Entity(ctx, "track", "a")
		if !got.Attrs["energy"].Equal(domain.Num(0.5)) {
			t.Error("changing the input after the write changed the store")
		}
		got.Attrs["energy"] = domain.Num(0)
		got.Attrs["extra"] = domain.Num(1)
		again, _ := s.Entity(ctx, "track", "a")
		if !again.Attrs["energy"].Equal(domain.Num(0.5)) || len(again.Attrs) != 3 {
			t.Error("changing a returned entity changed the store")
		}
		list, _ := s.ListEntities(ctx, "track")
		list[0].Attrs["energy"] = domain.Num(0)
		if again, _ := s.Entity(ctx, "track", "a"); !again.Attrs["energy"].Equal(domain.Num(0.5)) {
			t.Error("changing a listed entity changed the store")
		}
	})

	t.Run("a cancelled context is an error", func(t *testing.T) {
		s := newStore()
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if err := s.UpsertEntities(cancelled, []domain.Entity{track("a", 0)}); err == nil {
			t.Error("UpsertEntities ignored the context")
		}
		if n, _ := s.CountEntities(ctx); len(n) != 0 {
			t.Error("a cancelled write must not store anything")
		}
	})

	t.Run("it is safe for concurrent use", func(t *testing.T) {
		s := newStore()
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					id := fmt.Sprintf("t%d", i%10)
					_ = s.UpsertEntities(ctx, []domain.Entity{track(id, float64(w))})
					_, _ = s.Entity(ctx, "track", domain.EntityID(id))
					_, _ = s.ListEntities(ctx, "track")
					_, _ = s.CountEntities(ctx)
				}
			}()
		}
		wg.Wait()
		if n, _ := s.CountEntities(ctx); n["track"] != 10 {
			t.Errorf("counts = %v", n)
		}
	})

	t.Run("a user's interactions come back on their own, in arrival order", func(t *testing.T) {
		s := newStore()
		ts := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		_ = s.UpsertInteractions(ctx, []domain.Interaction{
			{User: "u1", Type: "play", Target: "a", TS: ts},
			{User: "u2", Type: "play", Target: "b", TS: ts},
			{User: "u1", Type: "save", Target: "c", TS: ts},
		})
		got, err := s.UserInteractions(ctx, "u1")
		if err != nil || len(got) != 2 || got[0].Target != "a" || got[1].Target != "c" {
			t.Errorf("u1 = %+v, %v", got, err)
		}
		if none, _ := s.UserInteractions(ctx, "nobody"); len(none) != 0 {
			t.Errorf("an unknown user has nothing: %+v", none)
		}
		got[0].Target = "changed"
		if again, _ := s.UserInteractions(ctx, "u1"); again[0].Target != "a" {
			t.Error("changing a returned interaction changed the store")
		}
	})

	t.Run("the version changes on every write and only then", func(t *testing.T) {
		s := newStore()
		v0, err := s.Version(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = s.ListEntities(ctx, "track")
		_, _ = s.Interactions(ctx)
		if v, _ := s.Version(ctx); v != v0 {
			t.Errorf("reading changed the version: %d -> %d", v0, v)
		}
		_ = s.UpsertEntities(ctx, []domain.Entity{track("a", 0.5)})
		v1, _ := s.Version(ctx)
		_ = s.UpsertInteractions(ctx, []domain.Interaction{{User: "u", Type: "play", Target: "a", TS: time.Now()}})
		v2, _ := s.Version(ctx)
		if v1 == v0 || v2 == v1 {
			t.Errorf("versions %d, %d, %d: each write must change it", v0, v1, v2)
		}
		_ = s.UpsertEntities(ctx, []domain.Entity{{Type: "track"}}) // refused
		if v, _ := s.Version(ctx); v != v2 {
			t.Errorf("a refused write changed the version: %d -> %d", v2, v)
		}
	})

	t.Run("interactions are stored in arrival order and can be filtered by type", func(t *testing.T) {
		s := newStore()
		ts := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		add := func(user, typ, target string) domain.Interaction {
			return domain.Interaction{User: domain.UserID(user), Type: typ, Target: domain.EntityID(target), TS: ts}
		}
		if err := s.UpsertInteractions(ctx, []domain.Interaction{add("u1", "play", "a"), add("u1", "save", "b"), add("u2", "play", "c")}); err != nil {
			t.Fatal(err)
		}
		all, _ := s.Interactions(ctx)
		if len(all) != 3 || all[0].Target != "a" || all[2].Target != "c" {
			t.Errorf("all = %+v", all)
		}
		plays, _ := s.Interactions(ctx, "play")
		if len(plays) != 2 {
			t.Errorf("plays = %+v", plays)
		}
		both, _ := s.Interactions(ctx, "play", "save")
		if len(both) != 3 {
			t.Errorf("both = %+v", both)
		}
		if none, _ := s.Interactions(ctx, "share"); len(none) != 0 {
			t.Errorf("none = %+v", none)
		}
		if n, _ := s.CountInteractions(ctx); n["play"] != 2 || n["save"] != 1 || len(n) != 2 {
			t.Errorf("counts = %v", n)
		}
	})

	t.Run("sending the same interaction again changes nothing, a different field is a new one", func(t *testing.T) {
		s := newStore()
		ts := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		x := domain.Interaction{User: "u", Type: "add_to_playlist", Target: "a", TS: ts, Fields: map[string]string{"playlist_id": "p1"}}
		y := x
		y.Fields = map[string]string{"playlist_id": "p2"}
		_ = s.UpsertInteractions(ctx, []domain.Interaction{x})
		_ = s.UpsertInteractions(ctx, []domain.Interaction{x, y})
		if n, _ := s.CountInteractions(ctx); n["add_to_playlist"] != 2 {
			t.Errorf("counts = %v", n)
		}
	})

	t.Run("a bad interaction stores nothing and callers cannot change what was stored", func(t *testing.T) {
		s := newStore()
		ts := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		good := domain.Interaction{User: "u", Type: "play", Target: "a", TS: ts, Fields: map[string]string{"k": "v"}}
		if err := s.UpsertInteractions(ctx, []domain.Interaction{good, {Type: "play", Target: "a"}}); err == nil {
			t.Fatal("an interaction without a user should be refused")
		}
		if n, _ := s.CountInteractions(ctx); len(n) != 0 {
			t.Errorf("a failed batch left %v behind", n)
		}
		_ = s.UpsertInteractions(ctx, []domain.Interaction{good})
		good.Fields["k"] = "changed"
		got, _ := s.Interactions(ctx)
		if got[0].Fields["k"] != "v" {
			t.Error("changing the input after the write changed the store")
		}
		got[0].Fields["k"] = "changed"
		if again, _ := s.Interactions(ctx); again[0].Fields["k"] != "v" {
			t.Error("changing a returned interaction changed the store")
		}
	})

	model := func(t *testing.T, items ...string) *factors.Model {
		t.Helper()
		ids := make([]domain.EntityID, len(items))
		vecs := make([]float32, 0, 2*len(items))
		for i, id := range items {
			ids[i] = domain.EntityID(id)
			vecs = append(vecs, float32(i), 1)
		}
		p := factors.Params{Factors: 2, Regularization: 1, Alpha: 1, Iterations: 1}
		m, err := factors.NewModel(p, ids, vecs)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	t.Run("a model that was never trained is ErrNotFound", func(t *testing.T) {
		s := newStore()
		if _, err := s.Model(ctx, "taste"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Model = %v, want ErrNotFound", err)
		}
	})

	t.Run("a saved model is read back and each save is the next version", func(t *testing.T) {
		s := newStore()
		first := model(t, "a", "b")
		v, err := s.SaveModel(ctx, "taste", first)
		if err != nil || v != 1 {
			t.Fatalf("first save = %d, %v; want version 1", v, err)
		}
		if first.Version != 0 {
			t.Errorf("SaveModel changed the caller's model: version %d", first.Version)
		}
		got, err := s.Model(ctx, "taste")
		if err != nil || got.Version != 1 || len(got.Items) != 2 || got.Items[1] != "b" || got.Vecs[2] != 1 {
			t.Fatalf("Model = %+v, %v", got, err)
		}
		if row, ok := got.Row("b"); !ok || row != 1 {
			t.Errorf("the stored model lost its item index: Row(b) = %d, %v", row, ok)
		}
		if v, _ := s.SaveModel(ctx, "taste", model(t, "a", "b", "c")); v != 2 {
			t.Errorf("second save = version %d, want 2", v)
		}
		if got, _ := s.Model(ctx, "taste"); got.Version != 2 || len(got.Items) != 3 {
			t.Errorf("the active model is %+v, want version 2 with 3 items", got)
		}
	})

	t.Run("models with different names are kept apart", func(t *testing.T) {
		s := newStore()
		_, _ = s.SaveModel(ctx, "taste", model(t, "a"))
		_, _ = s.SaveModel(ctx, "mood", model(t, "x", "y"))
		a, _ := s.Model(ctx, "taste")
		b, _ := s.Model(ctx, "mood")
		if len(a.Items) != 1 || len(b.Items) != 2 || a.Version != 1 || b.Version != 1 {
			t.Errorf("taste = %d items v%d, mood = %d items v%d", len(a.Items), a.Version, len(b.Items), b.Version)
		}
	})

	t.Run("saving a model changes the store version, so what was computed from the old one is stale", func(t *testing.T) {
		s := newStore()
		before, _ := s.Version(ctx)
		_, _ = s.SaveModel(ctx, "taste", model(t, "a"))
		after, _ := s.Version(ctx)
		if after == before {
			t.Error("the store version did not change when a model was saved")
		}
	})

	t.Run("a model needs a name and a body", func(t *testing.T) {
		s := newStore()
		if _, err := s.SaveModel(ctx, "", model(t, "a")); err == nil {
			t.Error("a model without a name was accepted")
		}
		if _, err := s.SaveModel(ctx, "taste", nil); err == nil {
			t.Error("a nil model was accepted")
		}
	})
}
