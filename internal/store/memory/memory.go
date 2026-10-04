package memory

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/store"
)

type InMemoryStore struct {
	mu       sync.RWMutex
	entities map[string]map[domain.EntityID]domain.Entity
}

var _ store.Store = (*InMemoryStore)(nil)

func New() *InMemoryStore {
	return &InMemoryStore{entities: map[string]map[domain.EntityID]domain.Entity{}}
}

func clone(e domain.Entity) domain.Entity {
	e.Attrs = maps.Clone(e.Attrs)
	if e.Attrs == nil {
		e.Attrs = map[string]domain.Value{}
	}
	return e
}

func (s *InMemoryStore) UpsertEntities(ctx context.Context, entities []domain.Entity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, e := range entities {
		if e.Type == "" || e.ID == "" {
			return fmt.Errorf("store: entity %d has no type or id", i)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entities {
		byID := s.entities[e.Type]
		if byID == nil {
			byID = map[domain.EntityID]domain.Entity{}
			s.entities[e.Type] = byID
		}
		byID[e.ID] = clone(e)
	}
	return nil
}

func (s *InMemoryStore) Entity(ctx context.Context, typ string, id domain.EntityID) (domain.Entity, error) {
	if err := ctx.Err(); err != nil {
		return domain.Entity{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entities[typ][id]
	if !ok {
		return domain.Entity{}, store.ErrNotFound
	}
	return clone(e), nil
}

func (s *InMemoryStore) Entities(ctx context.Context, typ string, ids []domain.EntityID) ([]domain.Entity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Entity, 0, len(ids))
	for _, id := range ids {
		if e, ok := s.entities[typ][id]; ok {
			out = append(out, clone(e))
		}
	}
	return out, nil
}

func (s *InMemoryStore) ListEntities(ctx context.Context, typ string) ([]domain.Entity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Entity, 0, len(s.entities[typ]))
	for _, e := range s.entities[typ] {
		out = append(out, clone(e))
	}
	slices.SortFunc(out, func(a, b domain.Entity) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return out, nil
}

func (s *InMemoryStore) CountEntities(ctx context.Context) (map[string]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]int{}
	for typ, byID := range s.entities {
		if len(byID) > 0 {
			out[typ] = len(byID)
		}
	}
	return out, nil
}
