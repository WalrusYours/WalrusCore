package schema

import (
	"strings"
	"testing"

	"github.com/timurcravtov/walrus/internal/domain"
)

func TestComputedExampleAttributes(t *testing.T) {
	s := load(t, "news.yml")
	c, err := s.Entities["article"].NewComputer()
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("word ", 1000)
	out, err := c.Apply(map[string]domain.Value{
		"title": domain.Str("Short headline"),
		"body":  domain.Str(body),
	})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := out["title_length"].AsFloat(); v != 14 {
		t.Errorf("title_length = %v, want 14", out["title_length"])
	}
	if v, _ := out["reading_minutes"].AsFloat(); v != 5 {
		t.Errorf("reading_minutes = %v, want 5", out["reading_minutes"])
	}
	if v, _ := out["length_class"].AsString(); v != "medium" {
		t.Errorf("length_class = %v, want medium", out["length_class"])
	}
}

func TestComputedSkipsWhenInputsMissing(t *testing.T) {
	c, err := load(t, "news.yml").Entities["article"].NewComputer()
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Apply(map[string]domain.Value{"title": domain.Str("only a title")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["title_length"]; !ok {
		t.Error("title_length should be computed")
	}
	for _, name := range []string{"reading_minutes", "length_class"} {
		if _, ok := out[name]; ok {
			t.Errorf("%s should be absent when body is missing", name)
		}
	}
}

func TestComputedRejectsSentValue(t *testing.T) {
	c, _ := load(t, "jobs.yml").Entities["job"].NewComputer()
	_, err := c.Apply(map[string]domain.Value{"skills": domain.Set("go"), "skill_count": domain.Num(99)})
	if err == nil || !strings.Contains(err.Error(), "must not be sent") {
		t.Errorf("err = %v, want a must-not-be-sent error", err)
	}
}

func TestIntIsRounded(t *testing.T) {
	e := EntitySpec{Attributes: map[string]AttributeSpec{
		"price": {Type: TypeFloat},
		"band":  {Type: TypeInt, Computed: "price / 10"},
	}}
	c, err := e.NewComputer()
	if err != nil {
		t.Fatal(err)
	}
	out, _ := c.Apply(map[string]domain.Value{"price": domain.Num(184)})
	if v, _ := out["band"].AsFloat(); v != 18 {
		t.Errorf("band = %v, want 18", out["band"])
	}
}

func TestNewComputerErrors(t *testing.T) {
	cases := map[string]EntitySpec{
		"unknown reference": {Attributes: map[string]AttributeSpec{
			"n": {Type: TypeInt, Computed: "len(nope)"}}},
		"bad expression": {Attributes: map[string]AttributeSpec{
			"n": {Type: TypeInt, Computed: "len("}}},
		"unsupported type": {Attributes: map[string]AttributeSpec{
			"s": {Type: TypeString}, "t": {Type: TypeSet, Computed: "s"}}},
		"self cycle": {Attributes: map[string]AttributeSpec{
			"a": {Type: TypeInt, Computed: "a + 1"}}},
		"cycle": {Attributes: map[string]AttributeSpec{
			"a": {Type: TypeInt, Computed: "b + 1"}, "b": {Type: TypeInt, Computed: "a + 1"}}},
	}
	for name, e := range cases {
		if _, err := e.NewComputer(); err == nil {
			t.Errorf("%s: NewComputer should fail", name)
		}
	}
}

func TestComputedTypeMismatch(t *testing.T) {
	e := EntitySpec{Attributes: map[string]AttributeSpec{
		"title": {Type: TypeString},
		"n":     {Type: TypeInt, Computed: "title"},
	}}
	c, err := e.NewComputer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Apply(map[string]domain.Value{"title": domain.Str("x")}); err == nil {
		t.Error("a string result for an int attribute should fail")
	}
}

func TestEveryExampleHasValidComputedAttributes(t *testing.T) {
	for _, name := range []string{"feed.yml", "marketplace.yml", "spotify.yml", "news.yml", "jobs.yml", "courses.yml"} {
		for et, e := range load(t, name).Entities {
			if _, err := e.NewComputer(); err != nil {
				t.Errorf("%s: entity %s: %v", name, et, err)
			}
		}
	}
}
