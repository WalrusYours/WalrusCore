package schema

import (
	"fmt"
	"math"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/geo"
)

type AttrType string

const (
	TypeCategorical AttrType = "categorical"
	TypeString      AttrType = "string"
	TypeFloat       AttrType = "float"
	TypeInt         AttrType = "int"
	TypeBool        AttrType = "bool"
	TypeTimestamp   AttrType = "timestamp"
	TypeSet         AttrType = "set"
	TypeVector      AttrType = "vector"
	TypeRef         AttrType = "ref"
	TypeGeo         AttrType = "geo" // a point: {"lat": .., "lon": ..}, kept as a two-number vector
)

var AttrTypes = []AttrType{
	TypeCategorical, TypeString, TypeFloat, TypeInt, TypeBool,
	TypeTimestamp, TypeSet, TypeVector, TypeRef, TypeGeo,
}

func (t AttrType) Valid() bool {
	for _, v := range AttrTypes {
		if t == v {
			return true
		}
	}
	return false
}

func (t AttrType) Kind() domain.Kind {
	switch t {
	case TypeCategorical, TypeString, TypeRef:
		return domain.KindString
	case TypeFloat, TypeInt:
		return domain.KindFloat
	case TypeBool:
		return domain.KindBool
	case TypeTimestamp:
		return domain.KindTime
	case TypeSet:
		return domain.KindSet
	case TypeVector, TypeGeo:
		return domain.KindVector
	}
	return domain.KindNull
}

// Coerce turns decoded JSON into the Value this attribute declares, or returns an error.
// It is the one place untyped ingest input becomes typed data.
func (a AttributeSpec) Coerce(raw any) (domain.Value, error) {
	if raw == nil {
		if a.Optional {
			return domain.Null(), nil
		}
		return domain.Null(), fmt.Errorf("value is required")
	}
	switch a.Type {
	case TypeCategorical, TypeString, TypeRef:
		s, ok := raw.(string)
		if !ok {
			return domain.Null(), fmt.Errorf("expected string, got %T", raw)
		}
		return domain.Str(s), nil

	case TypeFloat, TypeInt:
		f, ok := toFloat(raw)
		if !ok {
			return domain.Null(), fmt.Errorf("expected number, got %T", raw)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return domain.Null(), fmt.Errorf("number must be finite")
		}
		if a.Type == TypeInt && f != math.Trunc(f) {
			return domain.Null(), fmt.Errorf("expected integer, got %v", f)
		}
		if a.Range != nil && (f < a.Range[0] || f > a.Range[1]) {
			return domain.Null(), fmt.Errorf("%v is outside range [%v, %v]", f, a.Range[0], a.Range[1])
		}
		return domain.Num(f), nil

	case TypeBool:
		b, ok := raw.(bool)
		if !ok {
			return domain.Null(), fmt.Errorf("expected bool, got %T", raw)
		}
		return domain.Bool(b), nil

	case TypeTimestamp:
		switch t := raw.(type) {
		case time.Time:
			return domain.Time(t), nil
		case string:
			ts, err := time.Parse(time.RFC3339, t)
			if err != nil {
				return domain.Null(), fmt.Errorf("expected RFC 3339 timestamp, got %q", t)
			}
			return domain.Time(ts), nil
		}
		return domain.Null(), fmt.Errorf("expected timestamp, got %T", raw)

	case TypeSet:
		items, ok := raw.([]any)
		if !ok {
			if ss, isStrings := raw.([]string); isStrings {
				return domain.Set(ss...), nil
			}
			return domain.Null(), fmt.Errorf("expected array of strings, got %T", raw)
		}
		out := make([]string, 0, len(items))
		for i, it := range items {
			s, ok := it.(string)
			if !ok {
				return domain.Null(), fmt.Errorf("set element %d: expected string, got %T", i, it)
			}
			out = append(out, s)
		}
		return domain.Set(out...), nil

	case TypeGeo:
		return geo.Parse(raw)

	case TypeVector:
		items, ok := raw.([]any)
		if !ok {
			return domain.Null(), fmt.Errorf("expected array of numbers, got %T", raw)
		}
		if a.Dim > 0 && len(items) != a.Dim {
			return domain.Null(), fmt.Errorf("expected vector of length %d, got %d", a.Dim, len(items))
		}
		out := make([]float32, len(items))
		for i, it := range items {
			f, ok := toFloat(it)
			if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
				return domain.Null(), fmt.Errorf("vector element %d: expected finite number", i)
			}
			out[i] = float32(f)
		}
		return domain.Vec(out...), nil
	}
	return domain.Null(), fmt.Errorf("unknown attribute type %q", a.Type)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	}
	return 0, false
}
