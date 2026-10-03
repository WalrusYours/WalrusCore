// Package scoring turns user history into signals.
package scoring

import (
	"fmt"
	"math"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
)

// Profile is a user's sparse taste vector over items: the sum of edge weights, each decayed
// by 2^(-age / (scale · half_life)) (ALGORITHMS.md 2). scale is the horizon meta-parameter,
// 1 meaning the schema half-lives. Interaction types without a half-life do not decay.
// Negative schema weights (skip, hide) give negative entries.
func Profile(
	rules map[string]schema.Interaction,
	history []domain.Interaction,
	now time.Time,
	scale float64,
) (map[domain.EntityID]float64, error) {
	if scale <= 0 {
		return nil, fmt.Errorf("horizon scale must be positive, got %v", scale)
	}
	p := make(map[domain.EntityID]float64)
	for _, ix := range history {
		rule, ok := rules[ix.Type]
		if !ok {
			return nil, fmt.Errorf("interaction type %q is not in the schema", ix.Type)
		}
		w, err := rule.EdgeWeight(ix.Value)
		if err != nil {
			return nil, err
		}
		p[ix.Target] += w * decay(now.Sub(ix.TS), rule.HalfLife, scale)
	}
	return p, nil
}

func decay(age, halfLife time.Duration, scale float64) float64 {
	if halfLife <= 0 || age <= 0 {
		return 1
	}
	return math.Exp2(-float64(age) / (scale * float64(halfLife)))
}
