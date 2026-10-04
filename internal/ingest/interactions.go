package ingest

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
)

type RawInteraction struct {
	User   string         `json:"user"`
	Type   string         `json:"type"`
	Target string         `json:"target"`
	Value  *float64       `json:"value,omitempty"`
	TS     string         `json:"ts,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// Interactions checks each event against the pushed schema and stores the valid ones. A bad event
// is rejected on its own. An event without a timestamp is stamped with the time it arrived.
func (s *Service) Interactions(ctx context.Context, raws []RawInteraction) (*Result, error) {
	c := s.schema.Compiled()
	if c == nil {
		return nil, &Error{http.StatusConflict, "schema_missing", "no schema has been pushed yet"}
	}
	if len(raws) == 0 {
		return nil, &Error{http.StatusBadRequest, "validation_error", "send at least one interaction"}
	}
	if len(raws) > MaxBatch {
		return nil, &Error{http.StatusBadRequest, "validation_error", fmt.Sprintf("a batch holds at most %d interactions, got %d", MaxBatch, len(raws))}
	}

	now := time.Now().UTC()
	res := &Result{Rejected: []Rejection{}}
	var valid []domain.Interaction
	for i, r := range raws {
		x, err := convertInteraction(c.Schema, r, now)
		if err != nil {
			res.Rejected = append(res.Rejected, Rejection{Index: i, Entity: r.Type, ID: r.Target, Error: err.Error()})
			continue
		}
		valid = append(valid, x)
	}
	if len(valid) > 0 {
		if err := s.store.UpsertInteractions(ctx, valid); err != nil {
			return nil, err
		}
	}
	res.Accepted = len(valid)
	return res, nil
}

func convertInteraction(sch *schema.Schema, r RawInteraction, now time.Time) (domain.Interaction, error) {
	spec, ok := sch.Interactions[r.Type]
	if !ok {
		return domain.Interaction{}, fmt.Errorf("unknown interaction type %q (declared: %s)", r.Type, join(sortedKeys(sch.Interactions)))
	}
	if r.User == "" || len(r.User) > maxIDLen {
		return domain.Interaction{}, fmt.Errorf("user must be 1 to %d characters", maxIDLen)
	}
	if r.Target == "" || len(r.Target) > maxIDLen {
		return domain.Interaction{}, fmt.Errorf("target must be 1 to %d characters", maxIDLen)
	}
	if r.Value != nil && (math.IsNaN(*r.Value) || math.IsInf(*r.Value, 0)) {
		return domain.Interaction{}, fmt.Errorf("value must be a finite number")
	}

	ts := now
	if r.TS != "" {
		t, err := time.Parse(time.RFC3339, r.TS)
		if err != nil {
			return domain.Interaction{}, fmt.Errorf("ts %q is not an RFC 3339 timestamp", r.TS)
		}
		ts = t.UTC()
	}

	for name := range r.Fields {
		if _, ok := spec.Fields[name]; !ok {
			return domain.Interaction{}, fmt.Errorf("%s has no field %q (declared: %s)", r.Type, name, join(sortedKeys(spec.Fields)))
		}
	}
	var fields map[string]string
	for _, name := range sortedKeys(spec.Fields) {
		f := spec.Fields[name]
		raw, sent := r.Fields[name]
		if !sent || raw == nil {
			if !f.Optional {
				return domain.Interaction{}, fmt.Errorf("missing field %q", name)
			}
			continue
		}
		text, err := fieldText(f.Type, raw)
		if err != nil {
			return domain.Interaction{}, fmt.Errorf("field %q: %w", name, err)
		}
		if fields == nil {
			fields = map[string]string{}
		}
		fields[name] = text
	}
	return domain.Interaction{
		User: domain.UserID(r.User), Type: r.Type, Target: domain.EntityID(r.Target),
		Value: r.Value, TS: ts, Fields: fields,
	}, nil
}

// fieldText keeps an event field as text, the form it is stored and grouped by.
func fieldText(t schema.AttrType, raw any) (string, error) {
	v, err := value(schema.AttributeSpec{Type: t}, raw)
	if err != nil {
		return "", err
	}
	switch t {
	case schema.TypeCategorical, schema.TypeString, schema.TypeRef:
		s, _ := v.AsString()
		return s, nil
	case schema.TypeInt, schema.TypeFloat:
		f, _ := v.AsFloat()
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	case schema.TypeBool:
		b, _ := v.AsBool()
		return strconv.FormatBool(b), nil
	}
	return "", fmt.Errorf("a field of type %q is not supported", t)
}
