package memory

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/factors"
	"github.com/timurcravtov/walrus/internal/store"
)

type InMemoryStore struct {
	mu       sync.RWMutex
	entities map[string]map[domain.EntityID]domain.Entity

	interactions map[string]domain.Interaction // by identity key
	order        []string                      // keys in arrival order
	byUser       map[domain.UserID][]string    // keys per user, in arrival order
	models       map[string]*factors.Model     // the active trained model per name
	version      uint64
}

var _ store.Store = (*InMemoryStore)(nil)

func New() *InMemoryStore {
	return &InMemoryStore{
		entities:     map[string]map[domain.EntityID]domain.Entity{},
		interactions: map[string]domain.Interaction{},
		byUser:       map[domain.UserID][]string{},
		models:       map[string]*factors.Model{},
	}
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
	if len(entities) > 0 {
		s.version++
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

// An interaction is identified by who did what to which item when, plus its fields, so sending
// the same event again changes nothing.
func interactionKey(x domain.Interaction) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s|%d", x.User, x.Type, x.Target, x.TS.UnixNano())
	names := make([]string, 0, len(x.Fields))
	for k := range x.Fields {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Fprintf(&b, "|%s=%s", k, x.Fields[k])
	}
	return b.String()
}

func cloneInteraction(x domain.Interaction) domain.Interaction {
	x.Fields = maps.Clone(x.Fields)
	if x.Value != nil {
		v := *x.Value
		x.Value = &v
	}
	return x
}

func (s *InMemoryStore) UpsertInteractions(ctx context.Context, interactions []domain.Interaction) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, x := range interactions {
		if x.User == "" || x.Type == "" || x.Target == "" {
			return fmt.Errorf("store: interaction %d has no user, type or target", i)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range interactions {
		k := interactionKey(x)
		if _, seen := s.interactions[k]; !seen {
			s.order = append(s.order, k)
			s.byUser[x.User] = append(s.byUser[x.User], k)
		}
		s.interactions[k] = cloneInteraction(x)
	}
	if len(interactions) > 0 {
		s.version++
	}
	return nil
}

// Interactions returns the interactions of the given types in the order they arrived, or all of
// them when no type is given.
func (s *InMemoryStore) Interactions(ctx context.Context, types ...string) ([]domain.Interaction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Interaction, 0, len(s.order))
	for _, k := range s.order {
		x := s.interactions[k]
		if len(types) == 0 || slices.Contains(types, x.Type) {
			out = append(out, cloneInteraction(x))
		}
	}
	return out, nil
}

func (s *InMemoryStore) UserInteractions(ctx context.Context, user domain.UserID) ([]domain.Interaction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := s.byUser[user]
	out := make([]domain.Interaction, len(keys))
	for i, k := range keys {
		out[i] = cloneInteraction(s.interactions[k])
	}
	return out, nil
}

func (s *InMemoryStore) SaveModel(ctx context.Context, name string, m *factors.Model) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if name == "" || m == nil {
		return 0, fmt.Errorf("store: a model needs a name and a body")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := 1
	if old, ok := s.models[name]; ok {
		next = old.Version + 1
	}
	stored := *m // a copy: the caller keeps its own, with its own version
	stored.Version = next
	s.models[name] = &stored
	s.version++
	return next, nil
}

func (s *InMemoryStore) Model(ctx context.Context, name string) (*factors.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.models[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return m, nil // models never change once stored, so sharing the pointer is safe
}

func (s *InMemoryStore) Version(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version, nil
}

func (s *InMemoryStore) CountInteractions(ctx context.Context) (map[string]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]int{}
	for _, x := range s.interactions {
		out[x.Type]++
	}
	return out, nil
}
