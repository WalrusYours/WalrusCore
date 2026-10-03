package expr

import (
	"math"
	"testing"

	"github.com/timurcravtov/walrus/internal/domain"
)

func eval(t *testing.T, src string, env Env) domain.Value {
	t.Helper()
	x, err := Compile(src)
	if err != nil {
		t.Fatalf("Compile(%q): %v", src, err)
	}
	v, err := x.Eval(env)
	if err != nil {
		t.Fatalf("Eval(%q): %v", src, err)
	}
	return v
}

func TestKnobExpressions(t *testing.T) {
	cases := []struct {
		src  string
		x    float64
		want float64
	}{
		{"1 - x", 0.25, 0.75},
		{"x", 0.3, 0.3},
		{"0.3 + 0.5 * x", 1, 0.8},
		{"lerp(0.2, 4, x)", 0.5, 2.1},
		{"min(x, 0.5)", 0.9, 0.5},
		{"max(x, 0.5)", 0.1, 0.5},
		{"-x + 1", 0.25, 0.75},
		{"2 * (x + 1)", 1, 4},
		{"1 / x", 0, 0},
		{"clamp(x * 2, 0, 1)", 0.9, 1},
		{"0.5 * x", 1, 0.5},
	}
	for _, c := range cases {
		x, err := Compile(c.src)
		if err != nil {
			t.Fatalf("Compile(%q): %v", c.src, err)
		}
		got, err := x.EvalFloat(c.x)
		if err != nil || math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%q at x=%v = %v, %v; want %v", c.src, c.x, got, err, c.want)
		}
	}
}

func TestComputedAttributeExpressions(t *testing.T) {
	env := Env{
		"title": domain.Str("Hello wörld"),
		"body":  domain.Str("one two  three\nfour"),
		"tags":  domain.Set("road", "shimano"),
		"price": domain.Num(180),
		"vec":   domain.Vec(1, 2, 3),
	}
	num := func(src string, want float64) {
		t.Helper()
		if f, ok := eval(t, src, env).AsFloat(); !ok || math.Abs(f-want) > 1e-9 {
			t.Errorf("%q = %v, want %v", src, eval(t, src, env), want)
		}
	}
	num("len(title)", 11)
	num("words(body)", 4)
	num("len(tags)", 2)
	num("len(vec)", 3)
	num("round(price / 7)", 26)
	num("log1p(0)", 0)
	num("sqrt(16)", 4)
	num("abs(-3)", 3)

	str := func(src, want string) {
		t.Helper()
		if s, ok := eval(t, src, env).AsString(); !ok || s != want {
			t.Errorf("%q = %v, want %q", src, eval(t, src, env), want)
		}
	}
	str("lower('ABC')", "abc")
	str("upper(title)", "HELLO WÖRLD")
	str("title + '!'", "Hello wörld!")
	str("if(price > 100, 'high', 'low')", "high")

	boolean := func(src string, want bool) {
		t.Helper()
		if b, ok := eval(t, src, env).AsBool(); !ok || b != want {
			t.Errorf("%q = %v, want %v", src, eval(t, src, env), want)
		}
	}
	boolean("contains(tags, 'road')", true)
	boolean("contains(tags, 'mtb')", false)
	boolean("contains(title, 'wör')", true)
	boolean("price >= 180 && len(tags) == 2", true)
	boolean("price < 100 || !(len(tags) == 2)", false)
	boolean("title == 'Hello wörld'", true)
	boolean("title != 'x'", true)
	boolean("'a' < 'b'", true)
}

func TestNullPropagates(t *testing.T) {
	env := Env{"price": domain.Num(10)}
	for _, src := range []string{"len(missing)", "price + missing", "missing > 1", "words(missing)", "-missing"} {
		if v := eval(t, src, env); !v.IsNull() {
			t.Errorf("%q = %v, want null", src, v)
		}
	}
}

func TestVars(t *testing.T) {
	x, err := Compile("max(1, words(body) / 200) + len(title) + len(title)")
	if err != nil {
		t.Fatal(err)
	}
	got := x.Vars()
	if len(got) != 2 || got[0] != "body" || got[1] != "title" {
		t.Errorf("Vars = %v, want [body title]", got)
	}
}

// Schema v2 expressions name values by namespace: $item.rating, $context.hour, seed.size.
func TestNamespacedNames(t *testing.T) {
	x, err := Compile("$item.rating / 5 * if($context.hour >= 18, 1, 0.5) + seed.mean.energy - seed.size")
	if err != nil {
		t.Fatal(err)
	}
	got := x.Vars()
	want := []string{"$context.hour", "$item.rating", "seed.mean.energy", "seed.size"}
	if len(got) != len(want) {
		t.Fatalf("Vars = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Vars = %v, want %v", got, want)
		}
	}
	v, err := x.Eval(Env{
		"$item.rating": domain.Num(4), "$context.hour": domain.Num(20),
		"seed.mean.energy": domain.Num(0.5), "seed.size": domain.Num(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := v.AsFloat(); f != 4.0/5*1+0.5-2 {
		t.Errorf("value = %v", f)
	}
	for _, src := range []string{"$", "$1", "$.a", "a.", "a.1", "$item.", "seed..size"} {
		if _, err := Compile(src); err == nil {
			t.Errorf("Compile(%q) should fail", src)
		}
	}
}

func TestCompileErrors(t *testing.T) {
	for _, src := range []string{
		"", "1 +", "(1", "1)", "foo(1)", "len()", "len(1, 2)", "min()", "1 $ 2", "'unterminated",
		"1.2.3", "if(1)", "x y",
	} {
		if _, err := Compile(src); err == nil {
			t.Errorf("Compile(%q) should fail", src)
		}
	}
}

func TestEvalErrors(t *testing.T) {
	env := Env{"s": domain.Str("a"), "n": domain.Num(1), "b": domain.Bool(true)}
	for _, src := range []string{
		"s * 2", "n + s", "-s", "!n", "len(n)", "words(n)", "sqrt(s)", "contains(n, 'a')",
		"contains(s, n)", "if(n, 1, 2)", "n && b", "s < n", "min(s)",
	} {
		x, err := Compile(src)
		if err != nil {
			t.Fatalf("Compile(%q): %v", src, err)
		}
		if v, err := x.Eval(env); err == nil {
			t.Errorf("Eval(%q) = %v, want an error", src, v)
		}
	}
}

func TestEvalFloatRejectsNonNumbers(t *testing.T) {
	x, _ := Compile("'text'")
	if _, err := x.EvalFloat(0); err == nil {
		t.Error("EvalFloat should reject a string result")
	}
}
