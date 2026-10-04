package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/timurcravtov/walrus/internal/domain"
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
}
