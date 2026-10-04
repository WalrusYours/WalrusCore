package recommend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/schema/expr"
)

// This file is the request layer of POST /v1/recommenders/{recommender}/recommend: validate the
// request against the schema, assign an experiment variant, resolve knobs and weights, apply
// fallbacks, call the Ranker, and keep the list under a rec_id. Reading the store, candidate
// generation and scoring are the Ranker's job.

type Service struct {
	schema   *schema.Service
	ranker   Ranker
	profiles Profiles
	history  History
	now      func() time.Time
	results  *resultCache
	variants *variantCache
}

func NewService(svc *schema.Service, r Ranker) *Service {
	if r == nil {
		r = EmptyRanker{}
	}
	return &Service{
		schema: svc, ranker: r, now: time.Now,
		results: newResultCache(10_000, time.Minute), variants: newVariantCache(),
	}
}

func (s *Service) WithProfiles(p Profiles) *Service {
	s.profiles = p
	return s
}

func (s *Service) WithHistory(h History) *Service {
	s.history = h
	return s
}

const (
	defaultLimit = 30
	maxLimit     = 200
	maxSeedItems = 1000
)

// call is one Recommend request as it moves through the steps below.
type call struct {
	req     Request
	started time.Time
	now     time.Time
	recID   string

	base *view // the schema as pushed
	v    *view // the experiment variant's schema, or base

	name string // the recommender asked for
	used string // the one that answers: name, the holdout's, or a fallback
	spec schema.RecommenderSpec
	seed Seed
	cx   map[string]domain.Value
	env  expr.Env

	asg     *Assignment
	holdout bool
	tune    tuning

	limit    int
	exclude  map[domain.EntityID]bool
	provided map[string]map[domain.EntityID]float64
}

// Recommend runs one request.
func (s *Service) Recommend(ctx context.Context, req Request) (*Response, error) {
	c, err := s.start(req)
	if err != nil {
		return nil, err
	}
	if err := s.readHistory(ctx, c); err != nil {
		return nil, err
	}
	s.assignVariant(c)
	if err := s.resolveTuning(ctx, c); err != nil {
		return nil, err
	}
	if err := s.applyFallback(ctx, c); err != nil {
		return nil, err
	}
	if err := c.bound(); err != nil {
		return nil, err
	}
	out, err := s.ranker.Rank(ctx, c.rankInput())
	if err != nil {
		return nil, fail(500, "internal", "ranking failed: %v", err)
	}
	return s.respond(c, out), nil
}

// start finds the schema and the recommender, and checks the seed and the context.
func (s *Service) start(req Request) (*call, error) {
	compiled := s.schema.Compiled()
	if compiled == nil {
		return nil, fail(409, "schema_missing", "no schema has been pushed yet")
	}
	c := &call{req: req, started: time.Now(), now: s.now().UTC(), recID: newID("rec_"), name: req.Recommender}
	c.base = &view{sch: compiled.Schema, c: compiled}
	c.v, c.used = c.base, c.name

	spec, ok := specFor(c.base.sch, c.name)
	if !ok {
		msg := fmt.Sprintf("%q is not a recommender in this schema", c.name)
		if len(c.base.sch.Recommenders) > 0 && c.name == "default" {
			msg = "this schema declares its own recommenders; call one by name"
		}
		return nil, fail(404, "unknown_recommender", "%s", msg)
	}
	c.spec = spec

	seed, err := validateSeed(c.name, spec, req)
	if err != nil {
		return nil, err
	}
	c.seed = seed
	cx, err := checkContext(c.base.sch, req.Context, c.now)
	if err != nil {
		return nil, err
	}
	c.cx = cx
	return c, nil
}

// readHistory fills the values request-time expressions may read: seed.size, profile.size and
// $context.x. $user attributes are not read yet, so they are null.
func (s *Service) readHistory(ctx context.Context, c *call) error {
	size := 0
	if s.history != nil && c.seed.User != "" {
		n, err := s.history.ProfileSize(ctx, c.base.c, c.seed.User)
		if err != nil {
			return fail(500, "internal", "reading the user's history: %v", err)
		}
		size = n
	}
	c.env = expr.Env{
		"seed.size":    domain.Num(float64(c.seed.Size())),
		"profile.size": domain.Num(float64(size)),
	}
	for name, val := range c.cx {
		c.env["$context."+name] = val
	}
	return nil
}

// assignVariant puts the user in an experiment variant or the holdout. A holdout user is served the
// holdout recommender.
func (s *Service) assignVariant(c *call) {
	c.asg, c.v, c.holdout = s.assign(c.base, c.name, c.seed.User, c.recID, c.env)
	if c.holdout {
		c.used = c.base.sch.Holdout.Recommender
		c.spec, _ = specFor(c.base.sch, c.used)
	}
}

func (s *Service) resolveTuning(ctx context.Context, c *call) error {
	t, err := s.tuningFor(ctx, c.v, c.used, c.spec, c.req, c.env)
	if err != nil {
		return err
	}
	c.tune = t
	return nil
}

// applyFallback switches to the first fallback whose condition holds, once.
func (s *Service) applyFallback(ctx context.Context, c *call) error {
	if c.holdout {
		return nil
	}
	for _, f := range c.spec.Fallback {
		holds, _ := evalBool(c.v.c, f.When, c.env)
		if !holds {
			continue
		}
		if next, ok := specFor(c.v.sch, f.Use); ok {
			c.used, c.spec = f.Use, next
			return s.resolveTuning(ctx, c)
		}
		return nil
	}
	return nil
}

// bound settles the list size, the excluded items and the scores the host supplied.
func (c *call) bound() error {
	c.limit = c.req.Limit
	switch {
	case c.limit <= 0 && c.spec.Limit != nil:
		c.limit = c.spec.Limit.Default
	case c.limit <= 0:
		c.limit = defaultLimit
	}
	ceiling := maxLimit
	if c.spec.Limit != nil && c.spec.Limit.Max > 0 {
		ceiling = c.spec.Limit.Max
	}
	c.limit = min(c.limit, ceiling)

	c.exclude = make(map[domain.EntityID]bool, len(c.req.Exclude))
	for _, id := range c.req.Exclude {
		c.exclude[domain.EntityID(id)] = true
	}
	c.provided = make(map[string]map[domain.EntityID]float64, len(c.req.Scores))
	for name, scores := range c.req.Scores {
		m := make(map[domain.EntityID]float64, len(scores))
		for id, sc := range scores {
			if math.IsNaN(sc) || math.IsInf(sc, 0) {
				return fail(400, "validation_error", "scores.%s.%s must be a finite number", name, id)
			}
			m[domain.EntityID(id)] = sc
		}
		c.provided[name] = m
	}
	return nil
}

func (c *call) rankInput() RankInput {
	return RankInput{
		Recommender: c.used, Spec: c.spec, Compiled: c.v.c, Seed: c.seed, User: c.seed.User, Context: c.cx, Env: c.env,
		Weights: c.tune.weights, Meta: c.tune.meta, Limit: c.limit, Locale: c.req.Locale,
		Exclude: c.exclude, Provided: c.provided,
	}
}

// respond keeps the list for explain and shapes the answer: excluded items dropped, cut to the
// limit, reasons only when asked for.
func (s *Service) respond(c *call, out RankOutput) *Response {
	scored := make([]domain.ScoredItem, 0, len(out.Items))
	for _, it := range out.Items {
		if !c.exclude[it.Item] {
			scored = append(scored, it)
		}
	}
	entityType := ""
	if len(c.spec.For) > 0 {
		entityType = c.spec.For[0]
	}
	items := make([]Item, 0, min(c.limit, len(scored)))
	for _, it := range scored[:min(c.limit, len(scored))] {
		typ := out.Types[it.Item]
		if typ == "" {
			typ = entityType
		}
		item := Item{ID: string(it.Item), Type: typ, Score: it.Score}
		if c.req.Explain {
			item.Reason = out.Reasons[it.Item]
		}
		items = append(items, item)
	}

	s.results.put(&stored{
		RecID: c.recID, Recommender: c.name, Used: c.used, User: c.seed.User, Items: scored,
		Reasons: out.Reasons, Because: out.Because, Weights: c.tune.weights, Meta: c.tune.meta,
		Experiment: c.asg, Holdout: c.holdout, At: c.now,
	})
	return &Response{
		Recommender: c.name, Used: c.used, RecID: c.recID, Items: items, Weights: c.tune.weights,
		Knobs: c.tune.knobs, Clamped: c.tune.clamped, Experiment: c.asg, Holdout: c.holdout,
		CandidatesConsidered: out.Candidates, TookMS: float64(time.Since(c.started).Microseconds()) / 1000,
	}
}

// specFor finds a recommender in the schema. A schema without a `recommenders` section has the
// implicit one, `default`: the first recommendable entity, seed user, every signal and knob.
func specFor(sch *schema.Schema, name string) (schema.RecommenderSpec, bool) {
	if len(sch.Recommenders) == 0 {
		if name != "default" {
			return schema.RecommenderSpec{}, false
		}
		ranked, _ := sch.RankedAll()
		return schema.RecommenderSpec{For: ranked, Seed: schema.SeedUser}, true
	}
	r, ok := sch.Recommenders[name]
	return r, ok
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func evalNumber(c *schema.Compiled, src string, env expr.Env) (float64, error) {
	x, err := c.Expr(src)
	if err != nil {
		return 0, err
	}
	v, err := x.Eval(env)
	if err != nil {
		return 0, err
	}
	f, ok := v.AsFloat()
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%q is not a finite number", src)
	}
	return f, nil
}

func evalBool(c *schema.Compiled, src string, env expr.Env) (bool, error) {
	x, err := c.Expr(src)
	if err != nil {
		return false, err
	}
	v, err := x.Eval(env)
	if err != nil {
		return false, err
	}
	b, ok := v.AsBool()
	return ok && b, nil
}
