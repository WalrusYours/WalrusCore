// Package expr is the one expression language of the schema: knob maps ("1 - x") and
// computed attributes ("len(body)"). Expressions compile once and evaluate against named
// values.
package expr

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/timurcravtov/walrus/internal/domain"
)

type Env map[string]domain.Value

type Expr struct {
	root node
	vars []string
}

type node func(Env) (domain.Value, error)

// Vars returns the sorted names the expression refers to.
func (e *Expr) Vars() []string { return slices.Clone(e.vars) }

func (e *Expr) Eval(env Env) (domain.Value, error) { return e.root(env) }

// EvalFloat evaluates a knob expression with the variable x. The result must be a number.
func (e *Expr) EvalFloat(x float64) (float64, error) {
	v, err := e.root(Env{"x": domain.Num(x)})
	if err != nil {
		return 0, err
	}
	f, ok := v.AsFloat()
	if !ok {
		return 0, fmt.Errorf("expression result is %s, want a number", v.Kind())
	}
	return f, nil
}

func Compile(src string) (*Expr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, vars: map[string]struct{}{}}
	root, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, fmt.Errorf("unexpected %q", p.peek().text)
	}
	vars := make([]string, 0, len(p.vars))
	for v := range p.vars {
		vars = append(vars, v)
	}
	slices.Sort(vars)
	return &Expr{root: root, vars: vars}, nil
}

// ---- lexer ----

type tokKind int

const (
	tEOF tokKind = iota
	tNum
	tStr
	tID
	tOp
)

type token struct {
	kind tokKind
	text string
	num  float64
}

func lex(src string) ([]token, error) {
	var out []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case c >= '0' && c <= '9' || c == '.':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				j++
			}
			var f float64
			if _, err := fmt.Sscanf(src[i:j], "%g", &f); err != nil || strings.Count(src[i:j], ".") > 1 {
				return nil, fmt.Errorf("bad number %q", src[i:j])
			}
			out = append(out, token{kind: tNum, text: src[i:j], num: f})
			i = j
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(src) && src[j] != c {
				j++
			}
			if j >= len(src) {
				return nil, fmt.Errorf("unterminated string")
			}
			out = append(out, token{kind: tStr, text: src[i+1 : j]})
			i = j + 1
		case isIdentStart(c) || c == '$' && i+1 < len(src) && isIdentStart(src[i+1]):
			// A name: x, v, or a namespaced one such as $item.rating, $context.hour, seed.size.
			// Each dot must be followed by another name part, so "a." and "a.1" are not names.
			j := i + 1
			for j < len(src) {
				switch {
				case isIdentStart(src[j]) || src[j] >= '0' && src[j] <= '9':
					j++
				case src[j] == '.' && j+1 < len(src) && isIdentStart(src[j+1]):
					j += 2
				default:
					goto done
				}
			}
		done:
			out = append(out, token{kind: tID, text: src[i:j]})
			i = j
		default:
			if i+1 < len(src) {
				switch two := src[i : i+2]; two {
				case "==", "!=", "<=", ">=", "&&", "||":
					out = append(out, token{kind: tOp, text: two})
					i += 2
					continue
				}
			}
			if strings.ContainsRune("+-*/<>!(),", rune(c)) {
				out = append(out, token{kind: tOp, text: string(c)})
				i++
				continue
			}
			return nil, fmt.Errorf("unexpected %q", string(c))
		}
	}
	return append(out, token{kind: tEOF}), nil
}

// ---- parser ----

type parser struct {
	toks []token
	p    int
	vars map[string]struct{}
}

func (p *parser) peek() token { return p.toks[p.p] }
func (p *parser) next() token { t := p.toks[p.p]; p.p++; return t }
func (p *parser) isOp(s string) bool {
	t := p.peek()
	return t.kind == tOp && t.text == s
}

type binary func(a, b domain.Value) (domain.Value, error)

func (p *parser) or() (node, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.isOp("||") {
		p.next()
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(env Env) (domain.Value, error) { return logic(env, l, r, true) }
	}
	return left, nil
}

func (p *parser) and() (node, error) {
	left, err := p.equality()
	if err != nil {
		return nil, err
	}
	for p.isOp("&&") {
		p.next()
		right, err := p.equality()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(env Env) (domain.Value, error) { return logic(env, l, r, false) }
	}
	return left, nil
}

func (p *parser) level(next func() (node, error), ops map[string]binary) (node, error) {
	left, err := next()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tOp && ops[p.peek().text] != nil {
		fn := ops[p.next().text]
		right, err := next()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(env Env) (domain.Value, error) {
			a, err := l(env)
			if err != nil {
				return domain.Null(), err
			}
			b, err := r(env)
			if err != nil {
				return domain.Null(), err
			}
			return fn(a, b)
		}
	}
	return left, nil
}

func (p *parser) equality() (node, error) {
	return p.level(p.comparison, map[string]binary{"==": eq(false), "!=": eq(true)})
}

func (p *parser) comparison() (node, error) {
	return p.level(p.additive, map[string]binary{
		"<": cmp(func(c int) bool { return c < 0 }), ">": cmp(func(c int) bool { return c > 0 }),
		"<=": cmp(func(c int) bool { return c <= 0 }), ">=": cmp(func(c int) bool { return c >= 0 }),
	})
}

func (p *parser) additive() (node, error) {
	return p.level(p.multiplicative, map[string]binary{"+": add, "-": arith('-')})
}

func (p *parser) multiplicative() (node, error) {
	return p.level(p.unary, map[string]binary{"*": arith('*'), "/": arith('/')})
}

func (p *parser) unary() (node, error) {
	if p.isOp("-") || p.isOp("!") {
		op := p.next().text
		inner, err := p.unary()
		if err != nil {
			return nil, err
		}
		return func(env Env) (domain.Value, error) {
			v, err := inner(env)
			if err != nil || v.IsNull() {
				return domain.Null(), err
			}
			if op == "-" {
				f, ok := v.AsFloat()
				if !ok {
					return domain.Null(), fmt.Errorf("cannot negate %s", v.Kind())
				}
				return domain.Num(-f), nil
			}
			b, ok := v.AsBool()
			if !ok {
				return domain.Null(), fmt.Errorf("cannot apply ! to %s", v.Kind())
			}
			return domain.Bool(!b), nil
		}, nil
	}
	return p.primary()
}

func (p *parser) primary() (node, error) {
	t := p.next()
	switch t.kind {
	case tNum:
		v := domain.Num(t.num)
		return func(Env) (domain.Value, error) { return v, nil }, nil
	case tStr:
		v := domain.Str(t.text)
		return func(Env) (domain.Value, error) { return v, nil }, nil
	case tID:
		if p.isOp("(") {
			return p.call(t.text)
		}
		switch t.text {
		case "true", "false":
			v := domain.Bool(t.text == "true")
			return func(Env) (domain.Value, error) { return v, nil }, nil
		}
		p.vars[t.text] = struct{}{}
		name := t.text
		return func(env Env) (domain.Value, error) { return env[name], nil }, nil
	case tOp:
		if t.text == "(" {
			inner, err := p.or()
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
	}
	return nil, fmt.Errorf("unexpected %q", t.text)
}

func (p *parser) call(name string) (node, error) {
	f, ok := funcs[name]
	if !ok {
		return nil, fmt.Errorf("unknown function %q", name)
	}
	p.next() // (
	var args []node
	if !p.isOp(")") {
		for {
			a, err := p.or()
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
	if len(args) < f.min || len(args) > f.max {
		return nil, fmt.Errorf("%s() takes %s arguments, got %d", name, arity(f), len(args))
	}
	return func(env Env) (domain.Value, error) {
		vals := make([]domain.Value, len(args))
		for i, a := range args {
			v, err := a(env)
			if err != nil {
				return domain.Null(), err
			}
			if v.IsNull() && !f.nullOK {
				return domain.Null(), nil
			}
			vals[i] = v
		}
		return f.fn(vals)
	}, nil
}

func arity(f function) string {
	if f.min == f.max {
		return fmt.Sprint(f.min)
	}
	return fmt.Sprintf("%d-%d", f.min, f.max)
}

// ---- operators ----

func logic(env Env, l, r node, isOr bool) (domain.Value, error) {
	a, err := l(env)
	if err != nil || a.IsNull() {
		return domain.Null(), err
	}
	ab, ok := a.AsBool()
	if !ok {
		return domain.Null(), fmt.Errorf("&& and || need booleans, got %s", a.Kind())
	}
	if isOr == ab {
		return domain.Bool(ab), nil
	}
	b, err := r(env)
	if err != nil || b.IsNull() {
		return domain.Null(), err
	}
	bb, ok := b.AsBool()
	if !ok {
		return domain.Null(), fmt.Errorf("&& and || need booleans, got %s", b.Kind())
	}
	return domain.Bool(bb), nil
}

func eq(negate bool) binary {
	return func(a, b domain.Value) (domain.Value, error) {
		if a.IsNull() || b.IsNull() {
			return domain.Null(), nil
		}
		return domain.Bool(a.Equal(b) != negate), nil
	}
}

func cmp(ok func(int) bool) binary {
	return func(a, b domain.Value) (domain.Value, error) {
		if a.IsNull() || b.IsNull() {
			return domain.Null(), nil
		}
		if x, y, isNum := floats(a, b); isNum {
			return domain.Bool(ok(compareFloat(x, y))), nil
		}
		if x, ok1 := a.AsString(); ok1 {
			if y, ok2 := b.AsString(); ok2 {
				return domain.Bool(ok(strings.Compare(x, y))), nil
			}
		}
		return domain.Null(), fmt.Errorf("cannot compare %s with %s", a.Kind(), b.Kind())
	}
}

func compareFloat(x, y float64) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

func floats(a, b domain.Value) (float64, float64, bool) {
	x, ok1 := a.AsFloat()
	y, ok2 := b.AsFloat()
	return x, y, ok1 && ok2
}

func add(a, b domain.Value) (domain.Value, error) {
	if a.IsNull() || b.IsNull() {
		return domain.Null(), nil
	}
	if x, ok := a.AsString(); ok {
		if y, ok := b.AsString(); ok {
			return domain.Str(x + y), nil
		}
	}
	return arith('+')(a, b)
}

func arith(op byte) binary {
	return func(a, b domain.Value) (domain.Value, error) {
		if a.IsNull() || b.IsNull() {
			return domain.Null(), nil
		}
		x, y, ok := floats(a, b)
		if !ok {
			return domain.Null(), fmt.Errorf("cannot apply %c to %s and %s", op, a.Kind(), b.Kind())
		}
		switch op {
		case '+':
			return domain.Num(x + y), nil
		case '-':
			return domain.Num(x - y), nil
		case '*':
			return domain.Num(x * y), nil
		}
		if y == 0 {
			return domain.Num(0), nil
		}
		return domain.Num(x / y), nil
	}
}

// ---- functions ----

type function struct {
	min, max int
	nullOK   bool
	fn       func(a []domain.Value) (domain.Value, error)
}

func num1(name string, f func(float64) float64) function {
	return function{1, 1, false, func(a []domain.Value) (domain.Value, error) {
		x, ok := a[0].AsFloat()
		if !ok {
			return domain.Null(), fmt.Errorf("%s() needs a number, got %s", name, a[0].Kind())
		}
		return domain.Num(f(x)), nil
	}}
}

func nums(name string, a []domain.Value) ([]float64, error) {
	out := make([]float64, len(a))
	for i, v := range a {
		f, ok := v.AsFloat()
		if !ok {
			return nil, fmt.Errorf("%s() needs numbers, got %s", name, v.Kind())
		}
		out[i] = f
	}
	return out, nil
}

var funcs map[string]function

func init() {
	funcs = map[string]function{
		"abs":   num1("abs", math.Abs),
		"round": num1("round", math.Round),
		"sqrt":  num1("sqrt", math.Sqrt),
		"log1p": num1("log1p", math.Log1p),
		"min": {1, 8, false, func(a []domain.Value) (domain.Value, error) {
			f, err := nums("min", a)
			if err != nil {
				return domain.Null(), err
			}
			return domain.Num(slices.Min(f)), nil
		}},
		"max": {1, 8, false, func(a []domain.Value) (domain.Value, error) {
			f, err := nums("max", a)
			if err != nil {
				return domain.Null(), err
			}
			return domain.Num(slices.Max(f)), nil
		}},
		"clamp": {3, 3, false, func(a []domain.Value) (domain.Value, error) {
			f, err := nums("clamp", a)
			if err != nil {
				return domain.Null(), err
			}
			return domain.Num(math.Min(math.Max(f[0], f[1]), f[2])), nil
		}},
		"lerp": {3, 3, false, func(a []domain.Value) (domain.Value, error) {
			f, err := nums("lerp", a)
			if err != nil {
				return domain.Null(), err
			}
			return domain.Num(f[0] + (f[1]-f[0])*f[2]), nil
		}},
		"len": {1, 1, false, func(a []domain.Value) (domain.Value, error) {
			if s, ok := a[0].AsString(); ok {
				return domain.Num(float64(utf8.RuneCountInString(s))), nil
			}
			if s, ok := a[0].AsSet(); ok {
				return domain.Num(float64(len(s))), nil
			}
			if v, ok := a[0].AsVector(); ok {
				return domain.Num(float64(len(v))), nil
			}
			return domain.Null(), fmt.Errorf("len() needs a string, set or vector, got %s", a[0].Kind())
		}},
		"words": {1, 1, false, str1("words", func(s string) domain.Value { return domain.Num(float64(len(strings.Fields(s)))) })},
		"lower": {1, 1, false, str1("lower", func(s string) domain.Value { return domain.Str(strings.ToLower(s)) })},
		"upper": {1, 1, false, str1("upper", func(s string) domain.Value { return domain.Str(strings.ToUpper(s)) })},
		"contains": {2, 2, false, func(a []domain.Value) (domain.Value, error) {
			item, ok := a[1].AsString()
			if !ok {
				return domain.Null(), fmt.Errorf("contains() needs a string item, got %s", a[1].Kind())
			}
			if set, ok := a[0].AsSet(); ok {
				return domain.Bool(slices.Contains(set, item)), nil
			}
			if s, ok := a[0].AsString(); ok {
				return domain.Bool(strings.Contains(s, item)), nil
			}
			return domain.Null(), fmt.Errorf("contains() needs a set or string, got %s", a[0].Kind())
		}},
		"if": {3, 3, false, func(a []domain.Value) (domain.Value, error) {
			c, ok := a[0].AsBool()
			if !ok {
				return domain.Null(), fmt.Errorf("if() needs a boolean condition, got %s", a[0].Kind())
			}
			if c {
				return a[1], nil
			}
			return a[2], nil
		}},
	}
}

func str1(name string, f func(string) domain.Value) func([]domain.Value) (domain.Value, error) {
	return func(a []domain.Value) (domain.Value, error) {
		s, ok := a[0].AsString()
		if !ok {
			return domain.Null(), fmt.Errorf("%s() needs a string, got %s", name, a[0].Kind())
		}
		return f(s), nil
	}
}

func isIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
