package expr

import (
	"fmt"
	"math"
	"slices"
)

// CompileFloat: numeric expression of one variable as a plain func, no allocations.
func CompileFloat(src, variable string) (func(float64) float64, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &numParser{toks: toks, variable: variable}
	fn, err := p.sum()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, fmt.Errorf("unexpected %q", p.peek().text)
	}
	return fn, nil
}

type numFn = func(float64) float64

type numParser struct {
	toks     []token
	p        int
	variable string
}

func (p *numParser) peek() token { return p.toks[p.p] }
func (p *numParser) next() token { t := p.toks[p.p]; p.p++; return t }
func (p *numParser) isOp(s string) bool {
	t := p.peek()
	return t.kind == tOp && t.text == s
}

func (p *numParser) sum() (numFn, error) {
	left, err := p.product()
	if err != nil {
		return nil, err
	}
	for p.isOp("+") || p.isOp("-") {
		op := p.next().text
		right, err := p.product()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		if op == "+" {
			left = func(v float64) float64 { return l(v) + r(v) }
		} else {
			left = func(v float64) float64 { return l(v) - r(v) }
		}
	}
	return left, nil
}

func (p *numParser) product() (numFn, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.isOp("*") || p.isOp("/") {
		op := p.next().text
		right, err := p.unary()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		if op == "*" {
			left = func(v float64) float64 { return l(v) * r(v) }
		} else {
			left = func(v float64) float64 {
				d := r(v)
				if d == 0 {
					return 0
				}
				return l(v) / d
			}
		}
	}
	return left, nil
}

func (p *numParser) unary() (numFn, error) {
	if p.isOp("-") {
		p.next()
		inner, err := p.unary()
		if err != nil {
			return nil, err
		}
		return func(v float64) float64 { return -inner(v) }, nil
	}
	return p.primary()
}

func (p *numParser) primary() (numFn, error) {
	t := p.next()
	switch t.kind {
	case tNum:
		c := t.num
		return func(float64) float64 { return c }, nil
	case tID:
		if p.isOp("(") {
			return p.call(t.text)
		}
		if t.text == p.variable {
			return func(v float64) float64 { return v }, nil
		}
		return nil, fmt.Errorf("unknown name %q (only %s is available)", t.text, p.variable)
	case tOp:
		if t.text == "(" {
			inner, err := p.sum()
			if err != nil {
				return nil, err
			}
			if !p.isOp(")") {
				return nil, fmt.Errorf(`missing ")"`)
			}
			p.next()
			return inner, nil
		}
	case tEOF:
		return nil, fmt.Errorf("unexpected end of expression")
	case tStr:
		return nil, fmt.Errorf("text is not allowed in a numeric expression")
	}
	return nil, fmt.Errorf("unexpected %q", t.text)
}

func (p *numParser) call(name string) (numFn, error) {
	p.next() // (
	var args []numFn
	if !p.isOp(")") {
		for {
			a, err := p.sum()
			if err != nil {
				return nil, err
			}
			args = append(args, a)
			if !p.isOp(",") {
				break
			}
			p.next()
		}
	}
	if !p.isOp(")") {
		return nil, fmt.Errorf(`missing ")" in call to %s`, name)
	}
	p.next()

	need := func(n int) error {
		if len(args) != n {
			return fmt.Errorf("%s() takes %d arguments, got %d", name, n, len(args))
		}
		return nil
	}
	unary := func(f func(float64) float64) (numFn, error) {
		if err := need(1); err != nil {
			return nil, err
		}
		a := args[0]
		return func(v float64) float64 { return f(a(v)) }, nil
	}

	switch name {
	case "abs":
		return unary(math.Abs)
	case "round":
		return unary(math.Round)
	case "sqrt":
		return unary(math.Sqrt)
	case "log1p":
		return unary(math.Log1p)
	case "lerp":
		if err := need(3); err != nil {
			return nil, err
		}
		a, b, t := args[0], args[1], args[2]
		return func(v float64) float64 { x := a(v); return x + (b(v)-x)*t(v) }, nil
	case "clamp":
		if err := need(3); err != nil {
			return nil, err
		}
		x, lo, hi := args[0], args[1], args[2]
		return func(v float64) float64 { return math.Min(math.Max(x(v), lo(v)), hi(v)) }, nil
	case "min", "max":
		if len(args) < 1 || len(args) > 8 {
			return nil, fmt.Errorf("%s() takes 1-8 arguments, got %d", name, len(args))
		}
		args = slices.Clone(args)
		pick := math.Min
		if name == "max" {
			pick = math.Max
		}
		return func(v float64) float64 {
			out := args[0](v)
			for _, a := range args[1:] {
				out = pick(out, a(v))
			}
			return out
		}, nil
	}
	return nil, fmt.Errorf("%s() is not available in a numeric expression", name)
}
