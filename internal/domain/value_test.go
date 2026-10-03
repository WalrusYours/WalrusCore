package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestZeroValueIsNull(t *testing.T) {
	var v Value
	if !v.IsNull() || v.Kind() != KindNull {
		t.Fatalf("zero Value should be null, got %v", v.Kind())
	}
}

func TestAccessorsMatchKind(t *testing.T) {
	ts := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		v    Value
		kind Kind
		ok   func(Value) bool
	}{
		{"string", Str("bikes"), KindString, func(v Value) bool { s, ok := v.AsString(); return ok && s == "bikes" }},
		{"float", Num(4.5), KindFloat, func(v Value) bool { f, ok := v.AsFloat(); return ok && f == 4.5 }},
		{"bool", Bool(true), KindBool, func(v Value) bool { b, ok := v.AsBool(); return ok && b }},
		{"time", Time(ts), KindTime, func(v Value) bool { x, ok := v.AsTime(); return ok && x.Equal(ts) }},
		{"set", Set("b", "a"), KindSet, func(v Value) bool { s, ok := v.AsSet(); return ok && len(s) == 2 }},
		{"vector", Vec(1, 2, 3), KindVector, func(v Value) bool { x, ok := v.AsVector(); return ok && len(x) == 3 }},
	}
	for _, c := range cases {
		if c.v.Kind() != c.kind {
			t.Errorf("%s: kind = %v, want %v", c.name, c.v.Kind(), c.kind)
		}
		if !c.ok(c.v) {
			t.Errorf("%s: accessor failed", c.name)
		}
	}
	// Wrong-kind accessors report ok=false instead of panicking.
	if _, ok := Num(1).AsString(); ok {
		t.Error("AsString on a float should report ok=false")
	}
	if _, ok := Null().AsFloat(); ok {
		t.Error("AsFloat on null should report ok=false")
	}
}

func TestSetIsSortedAndDeduplicated(t *testing.T) {
	a := Set("road", "shimano", "road")
	b := Set("shimano", "road")
	if !a.Equal(b) {
		t.Fatalf("sets with the same members should be equal: %v vs %v", a, b)
	}
	got, _ := a.AsSet()
	if len(got) != 2 || got[0] != "road" || got[1] != "shimano" {
		t.Fatalf("set = %v, want [road shimano]", got)
	}
}

func TestValuesAreImmutable(t *testing.T) {
	in := []string{"a", "b"}
	v := Set(in...)
	in[0] = "z" // mutating the caller's slice must not change the Value
	out, _ := v.AsSet()
	out[1] = "y" // nor must mutating a returned copy
	again, _ := v.AsSet()
	if again[0] != "a" || again[1] != "b" {
		t.Fatalf("Value changed through aliasing: %v", again)
	}
}

func TestEqual(t *testing.T) {
	if Str("1").Equal(Num(1)) {
		t.Error("different kinds must not be equal")
	}
	if !Null().Equal(Null()) {
		t.Error("null equals null")
	}
	if !Vec(1, 2).Equal(Vec(1, 2)) || Vec(1, 2).Equal(Vec(1, 3)) {
		t.Error("vector equality")
	}
	a := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := a.In(time.FixedZone("x", 3600)) // same instant, different zone
	if !Time(a).Equal(Time(b)) {
		t.Error("times at the same instant should be equal")
	}
}

func TestMarshalJSON(t *testing.T) {
	cases := []struct {
		v    Value
		want string
	}{
		{Str("x"), `"x"`},
		{Num(2.5), `2.5`},
		{Bool(false), `false`},
		{Set("b", "a"), `["a","b"]`},
		{Vec(1, 2), `[1,2]`},
		{Null(), `null`},
		{Time(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)), `"2026-10-01T12:00:00Z"`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.v)
		if err != nil || string(got) != c.want {
			t.Errorf("Marshal(%v) = %s, %v; want %s", c.v, got, err, c.want)
		}
	}
	// Inside an Entity, attributes encode as natural JSON.
	e := Entity{Type: "post", ID: "p1", Attrs: map[string]Value{"topic": Str("go")}}
	b, err := json.Marshal(e.Attrs)
	if err != nil || string(b) != `{"topic":"go"}` {
		t.Errorf("attrs JSON = %s, %v", b, err)
	}
}
