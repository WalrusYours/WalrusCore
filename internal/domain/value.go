// Package domain holds the plain types shared by all layers: Value, Entity, Interaction, scores.
package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Kind uint8

const (
	KindNull Kind = iota
	KindString
	KindFloat
	KindBool
	KindTime
	KindSet
	KindVector
)

var kindNames = [...]string{"null", "string", "float", "bool", "time", "set", "vector"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "invalid"
}

// Value stores any attribute as one tagged union, so no per-schema Go types are needed.
// The zero Value is null. Values are immutable.
type Value struct {
	kind Kind
	str  string
	num  float64
	flag bool
	ts   time.Time
	set  []string
	vec  []float32
}

func Null() Value            { return Value{} }
func Str(s string) Value     { return Value{kind: KindString, str: s} }
func Num(f float64) Value    { return Value{kind: KindFloat, num: f} }
func Bool(b bool) Value      { return Value{kind: KindBool, flag: b} }
func Time(t time.Time) Value { return Value{kind: KindTime, ts: t.UTC()} }

func Set(items ...string) Value {
	s := slices.Clone(items)
	slices.Sort(s)
	return Value{kind: KindSet, set: slices.Compact(s)}
}

func Vec(v ...float32) Value { return Value{kind: KindVector, vec: slices.Clone(v)} }

func (v Value) Kind() Kind   { return v.kind }
func (v Value) IsNull() bool { return v.kind == KindNull }

func (v Value) AsString() (string, bool)  { return v.str, v.kind == KindString }
func (v Value) AsFloat() (float64, bool)  { return v.num, v.kind == KindFloat }
func (v Value) AsBool() (bool, bool)      { return v.flag, v.kind == KindBool }
func (v Value) AsTime() (time.Time, bool) { return v.ts, v.kind == KindTime }

func (v Value) AsSet() ([]string, bool) { return slices.Clone(v.set), v.kind == KindSet }

func (v Value) AsVector() ([]float32, bool) { return slices.Clone(v.vec), v.kind == KindVector }

func (v Value) Equal(o Value) bool {
	if v.kind != o.kind {
		return false
	}
	switch v.kind {
	case KindNull:
		return true
	case KindString:
		return v.str == o.str
	case KindFloat:
		return v.num == o.num
	case KindBool:
		return v.flag == o.flag
	case KindTime:
		return v.ts.Equal(o.ts)
	case KindSet:
		return slices.Equal(v.set, o.set)
	case KindVector:
		return slices.Equal(v.vec, o.vec)
	}
	return false
}

func (v Value) String() string {
	switch v.kind {
	case KindString:
		return strconv.Quote(v.str)
	case KindFloat:
		return strconv.FormatFloat(v.num, 'g', -1, 64)
	case KindBool:
		return strconv.FormatBool(v.flag)
	case KindTime:
		return v.ts.Format(time.RFC3339)
	case KindSet:
		return "{" + strings.Join(v.set, ",") + "}"
	case KindVector:
		return fmt.Sprintf("vector[%d]", len(v.vec))
	}
	return "null"
}

func (v Value) MarshalJSON() ([]byte, error) {
	switch v.kind {
	case KindString:
		return json.Marshal(v.str)
	case KindFloat:
		if math.IsNaN(v.num) || math.IsInf(v.num, 0) {
			return nil, fmt.Errorf("domain: cannot encode %v as JSON", v.num)
		}
		return json.Marshal(v.num)
	case KindBool:
		return json.Marshal(v.flag)
	case KindTime:
		return json.Marshal(v.ts.Format(time.RFC3339Nano))
	case KindSet:
		return json.Marshal(v.set)
	case KindVector:
		return json.Marshal(v.vec)
	}
	return []byte("null"), nil
}
