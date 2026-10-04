package recommend

import (
	"context"

	"github.com/timurcravtov/walrus/internal/apperr"
	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/schema/expr"
)

// Error is the error a request fails with: an HTTP status, a code and a message.
type Error = apperr.Error

var fail = apperr.New

// Request is the body of a recommend call. The seed field that applies depends on the
// recommender's `seed:`; a request never carries a recommender definition.
type Request struct {
	Recommender string                        `json:"-"`
	User        string                        `json:"user,omitempty"`
	Item        string                        `json:"item,omitempty"`
	Items       []string                      `json:"items,omitempty"`
	Session     []string                      `json:"session,omitempty"`
	Users       []string                      `json:"users,omitempty"`
	Context     map[string]any                `json:"context,omitempty"`
	Limit       int                           `json:"limit,omitempty"`
	Exclude     []string                      `json:"exclude,omitempty"`
	Knobs       map[string]float64            `json:"knobs,omitempty"`
	Preset      string                        `json:"preset,omitempty"`
	Locale      string                        `json:"locale,omitempty"`
	Explain     bool                          `json:"explain,omitempty"`
	Scores      map[string]map[string]float64 `json:"scores,omitempty"`
}

// Seed is what a recommendation is relative to; exactly the field the seed type needs is set.
type Seed struct {
	Kind  string
	User  domain.UserID
	Item  domain.EntityID
	Items []domain.EntityID // items and session seeds
	Users []domain.UserID
}

// Size is how many things the seed holds, for `seed.size` in expressions.
func (s Seed) Size() int {
	switch s.Kind {
	case schema.SeedItem:
		return 1
	case schema.SeedItems, schema.SeedSession:
		return len(s.Items)
	case schema.SeedUsers:
		return len(s.Users)
	}
	return 0
}

type Item struct {
	ID     string  `json:"id"`
	Type   string  `json:"type"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason,omitempty"`
}

type Assignment struct {
	ID      string `json:"id"`
	Variant string `json:"variant"`
}

type Response struct {
	Recommender          string             `json:"recommender"`
	Used                 string             `json:"used"` // differs from Recommender when a fallback answered
	RecID                string             `json:"rec_id"`
	Items                []Item             `json:"items"`
	Weights              map[string]float64 `json:"weights"`
	Knobs                map[string]float64 `json:"knobs"`
	Clamped              []string           `json:"clamped"`
	Experiment           *Assignment        `json:"experiment,omitempty"`
	Holdout              bool               `json:"holdout,omitempty"`
	CandidatesConsidered int                `json:"candidates_considered"`
	TookMS               float64            `json:"took_ms"`
}

// RankInput is everything a Ranker needs; every field is already validated and resolved.
type RankInput struct {
	Recommender string
	Spec        schema.RecommenderSpec
	Compiled    *schema.Compiled // the variant's schema when an experiment applies
	Seed        Seed
	User        domain.UserID // the requesting user, when given
	Context     map[string]domain.Value
	// Env is what the schema's conditions read: seed.size, profile.size and $context values.
	Env      expr.Env
	Weights  map[string]float64 // signal id to weight, for the recommender's signals
	Meta     map[string]float64 // resolved meta-parameters (blend_user, seed_aggregate...)
	Limit    int
	Locale   string
	Exclude  map[domain.EntityID]bool
	Provided map[string]map[domain.EntityID]float64
}

type RankOutput struct {
	Items   []domain.ScoredItem // best first, with per-signal contributions in Signals
	Reasons map[domain.EntityID]string
	// Because says, per item and signal, what the signal found: the words behind that signal's share
	// of the score. Signals that contributed nothing have no entry.
	Because    map[domain.EntityID]map[string]string
	Types      map[domain.EntityID]string
	Candidates int
}

// Ranker generates candidates and scores them: candidate generation, constraints, scoring and rules.
type Ranker interface {
	Rank(ctx context.Context, in RankInput) (RankOutput, error)
}

// EmptyRanker ranks nothing. A Service given no Ranker uses it, which keeps request-layer tests free
// of a store.
type EmptyRanker struct{}

func (EmptyRanker) Rank(context.Context, RankInput) (RankOutput, error) { return RankOutput{}, nil }

// Profiles supplies a user's saved knob values. Optional: without it, saved values are empty.
type Profiles interface {
	SavedKnobs(ctx context.Context, user domain.UserID) (map[string]float64, error)
}

// History says how much a user has done, for `profile.size` in expressions (an empty profile can
// fall back to a non-personal recommender). Optional: without it, profile.size is 0.
type History interface {
	ProfileSize(ctx context.Context, c *schema.Compiled, user domain.UserID) (int, error)
}
