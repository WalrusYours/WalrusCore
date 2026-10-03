package expr

import (
	"math"
	"testing"
)

// The fast path must agree with the general evaluator on every numeric expression.
func TestCompileFloatAgreesWithEval(t *testing.T) {
	sources := []string{
		"x", "1 - x", "x * 2 + 1", "0.3 + 0.5 * x", "lerp(0.2, 4, x)", "lerp(0.15, 4, x)",
		"min(x, 0.5)", "max(x, 0.5, 0.2)", "clamp(x * 2, 0, 1)", "-x + 1", "2 * (x + 1)",
		"1 / x", "abs(x - 0.5)", "round(x * 10) / 10", "sqrt(x)", "log1p(x)", "1 - min(x, 30) / 30",
		"x / (x - 0.5)", "lerp(0, 1, lerp(0, 1, x))",
	}
	grid := []float64{0, 0.01, 0.25, 0.5, 0.75, 1, 2, 30, 100}
	for _, src := range sources {
		fast, err := CompileFloat(src, "x")
		if err != nil {
			t.Fatalf("CompileFloat(%q): %v", src, err)
		}
		slow, err := Compile(src)
		if err != nil {
			t.Fatalf("Compile(%q): %v", src, err)
		}
		for _, x := range grid {
			want, err := slow.EvalFloat(x)
			if err != nil {
				t.Fatalf("EvalFloat(%q, %v): %v", src, x, err)
			}
			got := fast(x)
			if math.IsNaN(want) && math.IsNaN(got) {
				continue
			}
			if math.Abs(got-want) > 1e-9 {
				t.Errorf("%q at x=%v: fast = %v, eval = %v", src, x, got, want)
			}
		}
	}
}

func TestCompileFloatUsesTheGivenVariable(t *testing.T) {
	f, err := CompileFloat("1 - min(v, 30) / 30", "v")
	if err != nil {
		t.Fatal(err)
	}
	if got := f(15); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("f(15) = %v, want 0.5", got)
	}
	if _, err := CompileFloat("x + 1", "v"); err == nil {
		t.Error("x is not the variable here, it should be an unknown name")
	}
}

func TestCompileFloatRejectsNonNumeric(t *testing.T) {
	for _, src := range []string{
		"", "1 +", "(1", "1)", "'text'", "x > 1", "x && 1", "!x", "if(x, 1, 2)", "len(x)", "words(x)",
		"foo(x)", "lerp(1, 2)", "abs()", "abs(1, 2)", "min()", "y", "true", "x y",
	} {
		if _, err := CompileFloat(src, "x"); err == nil {
			t.Errorf("CompileFloat(%q) should fail", src)
		}
	}
}

func TestCompileFloatDivisionByZeroIsZero(t *testing.T) {
	f, _ := CompileFloat("5 / x", "x")
	if got := f(0); got != 0 {
		t.Errorf("5/0 = %v, want 0", got)
	}
}

func TestCompileFloatDoesNotAllocate(t *testing.T) {
	f, err := CompileFloat("lerp(0.2, 4, x) + 0.5 * x", "x")
	if err != nil {
		t.Fatal(err)
	}
	var sink float64
	allocs := testing.AllocsPerRun(1000, func() { sink += f(0.37) })
	_ = sink
	if allocs != 0 {
		t.Errorf("allocations per call = %v, want 0", allocs)
	}
}

func BenchmarkEvalFloat(b *testing.B) {
	x, _ := Compile("lerp(0.2, 4, x)")
	for i := 0; i < b.N; i++ {
		_, _ = x.EvalFloat(0.37)
	}
}

func BenchmarkCompileFloat(b *testing.B) {
	f, _ := CompileFloat("lerp(0.2, 4, x)", "x")
	var sink float64
	for i := 0; i < b.N; i++ {
		sink += f(0.37)
	}
	_ = sink
}
