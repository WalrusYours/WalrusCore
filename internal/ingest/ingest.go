package ingest

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/timurcravtov/walrus/internal/apperr"
	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store"
)

const (
	MaxBatch = 1000
	maxIDLen = 200
)

// Error is the error a batch fails with as a whole: an HTTP status, a code and a message.
type Error = apperr.Error

type Raw struct {
	Entity     string         `json:"entity"`
	ID         string         `json:"id"`
	Attributes map[string]any `json:"attributes"`
}

type Rejection struct {
	Index  int    `json:"index"`
	Entity string `json:"entity"`
	ID     string `json:"id"`
	Error  string `json:"error"`
}

type Result struct {
	Accepted int         `json:"accepted"`
	Rejected []Rejection `json:"rejected"`
}

type Service struct {
	schema *schema.Service
	store  store.Store
}

func NewService(sch *schema.Service, st store.Store) *Service {
	return &Service{schema: sch, store: st}
}

// Entities checks each entity against the pushed schema and stores the valid ones. A bad entity
// is rejected on its own; the rest of the batch still goes in.
func (s *Service) Entities(ctx context.Context, raws []Raw) (*Result, error) {
	c := s.schema.Compiled()
	if c == nil {
		return nil, apperr.New(http.StatusConflict, "schema_missing", "no schema has been pushed yet")
	}
	if len(raws) == 0 {
		return nil, apperr.New(http.StatusBadRequest, "validation_error", "send at least one entity")
	}
	if len(raws) > MaxBatch {
		return nil, apperr.New(http.StatusBadRequest, "validation_error", "a batch holds at most %d entities, got %d", MaxBatch, len(raws))
	}

	res := &Result{Rejected: []Rejection{}}
	var valid []domain.Entity
	for i, r := range raws {
		e, err := convert(c.Schema, r)
		if err != nil {
			res.Rejected = append(res.Rejected, Rejection{Index: i, Entity: r.Entity, ID: r.ID, Error: err.Error()})
			continue
		}
		valid = append(valid, e)
	}
	if len(valid) > 0 {
		if err := s.store.UpsertEntities(ctx, valid); err != nil {
			return nil, err
		}
	}
	res.Accepted = len(valid)
	return res, nil
}

func convert(sch *schema.Schema, r Raw) (domain.Entity, error) {
	spec, ok := sch.Entities[r.Entity]
	if !ok {
		return domain.Entity{}, fmt.Errorf("unknown entity type %q (declared: %s)", r.Entity, strings.Join(sortedKeys(sch.Entities), ", "))
	}
	if r.ID == "" || len(r.ID) > maxIDLen {
		return domain.Entity{}, fmt.Errorf("id must be 1 to %d characters", maxIDLen)
	}

	for name := range r.Attributes {
		a, ok := spec.Attributes[name]
		switch {
		case !ok:
			return domain.Entity{}, fmt.Errorf("%s has no attribute %q (declared: %s)", r.Entity, name, strings.Join(sortedKeys(spec.Attributes), ", "))
		case a.Computed != "":
			return domain.Entity{}, fmt.Errorf("attribute %q is computed by WALRUS and cannot be sent", name)
		}
	}

	attrs := make(map[string]domain.Value, len(spec.Attributes))
	for _, name := range sortedKeys(spec.Attributes) {
		a := spec.Attributes[name]
		raw, sent := r.Attributes[name]
		if !sent || raw == nil {
			if !a.Optional && a.Computed == "" {
				return domain.Entity{}, fmt.Errorf("missing attribute %q", name)
			}
			continue
		}
		v, err := a.Coerce(raw)
		if err != nil {
			return domain.Entity{}, fmt.Errorf("attribute %q: %w", name, err)
		}
		attrs[name] = v
	}
	return domain.Entity{Type: r.Entity, ID: domain.EntityID(r.ID), Attrs: attrs}, nil
}

func join(s []string) string { return strings.Join(s, ", ") }

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }
