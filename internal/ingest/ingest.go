package ingest

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store"
)

const (
	MaxBatch = 1000
	maxIDLen = 200
)

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

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
		return nil, &Error{http.StatusConflict, "schema_missing", "no schema has been pushed yet"}
	}
	if len(raws) == 0 {
		return nil, &Error{http.StatusBadRequest, "validation_error", "send at least one entity"}
	}
	if len(raws) > MaxBatch {
		return nil, &Error{http.StatusBadRequest, "validation_error", fmt.Sprintf("a batch holds at most %d entities, got %d", MaxBatch, len(raws))}
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
		v, err := value(a, raw)
		if err != nil {
			return domain.Entity{}, fmt.Errorf("attribute %q: %w", name, err)
		}
		attrs[name] = v
	}
	return domain.Entity{Type: r.Entity, ID: domain.EntityID(r.ID), Attrs: attrs}, nil
}

func value(a schema.AttributeSpec, raw any) (domain.Value, error) {
	switch a.Type {
	case schema.TypeCategorical, schema.TypeString, schema.TypeRef:
		s, ok := raw.(string)
		if !ok {
			return domain.Value{}, fmt.Errorf("expected a string, got %s", describe(raw))
		}
		return domain.Str(s), nil

	case schema.TypeFloat, schema.TypeInt:
		f, ok := raw.(float64)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return domain.Value{}, fmt.Errorf("expected a number, got %s", describe(raw))
		}
		if a.Type == schema.TypeInt && f != math.Trunc(f) {
			return domain.Value{}, fmt.Errorf("expected a whole number, got %v", f)
		}
		if a.Range != nil && (f < a.Range[0] || f > a.Range[1]) {
			return domain.Value{}, fmt.Errorf("%v is outside the range [%v, %v]", f, a.Range[0], a.Range[1])
		}
		return domain.Num(f), nil

	case schema.TypeBool:
		b, ok := raw.(bool)
		if !ok {
			return domain.Value{}, fmt.Errorf("expected true or false, got %s", describe(raw))
		}
		return domain.Bool(b), nil

	case schema.TypeTimestamp:
		s, ok := raw.(string)
		if !ok {
			return domain.Value{}, fmt.Errorf("expected an RFC 3339 timestamp, got %s", describe(raw))
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return domain.Value{}, fmt.Errorf("%q is not an RFC 3339 timestamp", s)
		}
		return domain.Time(t), nil

	case schema.TypeSet:
		list, ok := raw.([]any)
		if !ok {
			return domain.Value{}, fmt.Errorf("expected a list of strings, got %s", describe(raw))
		}
		items := make([]string, len(list))
		for i, x := range list {
			s, ok := x.(string)
			if !ok {
				return domain.Value{}, fmt.Errorf("item %d: expected a string, got %s", i, describe(x))
			}
			items[i] = s
		}
		return domain.Set(items...), nil

	case schema.TypeVector:
		list, ok := raw.([]any)
		if !ok {
			return domain.Value{}, fmt.Errorf("expected a list of numbers, got %s", describe(raw))
		}
		if a.Dim > 0 && len(list) != a.Dim {
			return domain.Value{}, fmt.Errorf("expected %d numbers, got %d", a.Dim, len(list))
		}
		vec := make([]float32, len(list))
		for i, x := range list {
			f, ok := x.(float64)
			if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
				return domain.Value{}, fmt.Errorf("item %d: expected a number, got %s", i, describe(x))
			}
			vec[i] = float32(f)
		}
		return domain.Vec(vec...), nil
	}
	return domain.Value{}, fmt.Errorf("unsupported attribute type %q", a.Type)
}

func describe(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case float64:
		return "a number"
	case bool:
		return "a boolean"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", v)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
