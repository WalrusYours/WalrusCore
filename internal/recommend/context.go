package recommend

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
)

// checkContext validates a request's context against the schema's `context` block, like ingest
// validates entities: unknown fields and wrong types are rejected, a field that is not optional
// is required, and derived fields (hour, weekday, month) are filled from the request time (UTC)
// and cannot be sent.
func checkContext(sch *schema.Schema, in map[string]any, now time.Time) (map[string]domain.Value, error) {
	out := make(map[string]domain.Value, len(sch.Context))
	for _, name := range slices.Sorted(maps.Keys(in)) {
		f, ok := sch.Context[name]
		if !ok {
			return nil, fail(400, "unknown_context_field", "context.%s is not declared in the schema (declared: %v)", name, slices.Sorted(maps.Keys(sch.Context)))
		}
		if f.Derive != "" {
			return nil, fail(400, "validation_error", "context.%s is derived from the request time and cannot be sent", name)
		}
		v, err := contextValue(f, in[name])
		if err != nil {
			return nil, fail(400, "validation_error", "context.%s: %v", name, err)
		}
		out[name] = v
	}
	for _, name := range slices.Sorted(maps.Keys(sch.Context)) {
		f := sch.Context[name]
		switch {
		case f.Derive != "":
			out[name] = domain.Num(derive(f.Derive, now))
		case !f.Optional:
			if _, sent := in[name]; !sent {
				return nil, fail(400, "validation_error", "context.%s is required", name)
			}
		}
	}
	return out, nil
}

func derive(kind string, t time.Time) float64 {
	switch kind {
	case "hour":
		return float64(t.Hour())
	case "weekday": // 1 Monday .. 7 Sunday
		if t.Weekday() == time.Sunday {
			return 7
		}
		return float64(t.Weekday())
	}
	return float64(t.Month())
}

func contextValue(f schema.ContextField, raw any) (domain.Value, error) {
	switch f.Type {
	case schema.TypeCategorical, schema.TypeString:
		s, ok := raw.(string)
		if !ok {
			return domain.Null(), fmt.Errorf("expected a string")
		}
		if len(f.Values) > 0 && !slices.Contains(f.Values, s) {
			return domain.Null(), fmt.Errorf("%q is not one of %v", s, f.Values)
		}
		return domain.Str(s), nil
	case schema.TypeInt, schema.TypeFloat:
		n, ok := raw.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return domain.Null(), fmt.Errorf("expected a number")
		}
		if f.Type == schema.TypeInt && n != math.Trunc(n) {
			return domain.Null(), fmt.Errorf("expected a whole number, got %v", n)
		}
		if f.Range != nil && (n < f.Range[0] || n > f.Range[1]) {
			return domain.Null(), fmt.Errorf("%v is outside the range [%v, %v]", n, f.Range[0], f.Range[1])
		}
		return domain.Num(n), nil
	case schema.TypeBool:
		b, ok := raw.(bool)
		if !ok {
			return domain.Null(), fmt.Errorf("expected true or false")
		}
		return domain.Bool(b), nil
	case schema.TypeTimestamp:
		s, ok := raw.(string)
		if !ok {
			return domain.Null(), fmt.Errorf("expected an RFC 3339 time")
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return domain.Null(), fmt.Errorf("expected an RFC 3339 time such as 2026-10-03T12:00:00Z")
		}
		return domain.Time(t), nil
	case schema.TypeSet:
		list, ok := raw.([]any)
		if !ok {
			return domain.Null(), fmt.Errorf("expected a list of strings")
		}
		items := make([]string, len(list))
		for i, x := range list {
			s, ok := x.(string)
			if !ok {
				return domain.Null(), fmt.Errorf("expected a list of strings")
			}
			items[i] = s
		}
		return domain.Set(items...), nil
	}
	return domain.Null(), fmt.Errorf("unsupported type %s", f.Type)
}
