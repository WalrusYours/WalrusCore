package schema

import "github.com/timurcravtov/walrus/internal/schema/expr"

// Expr returns an expression of this schema compiled, compiling each distinct source once. A
// compiled schema never changes, so neither do its expressions; this is safe for concurrent use.
func (c *Compiled) Expr(src string) (*expr.Expr, error) {
	if e, ok := c.exprs.Load(src); ok {
		return e.(*expr.Expr), nil
	}
	e, err := expr.Compile(src)
	if err != nil {
		return nil, err
	}
	c.exprs.Store(src, e)
	return e, nil
}
