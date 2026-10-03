package recommend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/knobs"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/schema/expr"
)

// This file is the request layer of POST /v1/recommenders/{recommender}/recommend: resolve and validate the request against the
// tenant's schema, assign an experiment variant, resolve knobs and weights, apply fallbacks,
// call the Ranker, and store the list under a rec_id. Candidate generation and scoring sit
// behind Ranker because they need the store, which does not exist yet.

// Error is a failure with the HTTP status and code the API answers with.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func fail(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

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
	Kind    string
	User    domain.UserID
	Item    domain.EntityID
	Items   []domain.EntityID // items and session seeds
	Users   []domain.UserID
	present bool
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
	Schema      *schema.Schema // the variant's schema when an experiment applies
	Seed        Seed
	User        domain.UserID // the requesting user, when given
	Context     map[string]domain.Value
	Weights     map[string]float64 // signal id to weight, for the recommender's signals
	Meta        map[string]float64 // resolved meta-parameters (blend_user, seed_aggregate...)
	Limit       int
	Exclude     map[domain.EntityID]bool
	Provided    map[string]map[domain.EntityID]float64
}

type RankOutput struct {
	Items      []domain.ScoredItem // best first, with per-signal contributions in Signals
	Reasons    map[domain.EntityID]string
	Types      map[domain.EntityID]string
	Candidates int
}

// Ranker generates candidates and scores them: candidate generation, scoring, rules and re-ranking. The real one
// needs the store and the precompute, which are not built yet.
type Ranker interface {
	Rank(ctx context.Context, in RankInput) (RankOutput, error)
}

// EmptyRanker is used until a store exists: with nothing ingested there is nothing to
// recommend, and an empty list is the correct answer.
type EmptyRanker struct{}

func (EmptyRanker) Rank(context.Context, RankInput) (RankOutput, error) { return RankOutput{}, nil }

// Profiles supplies a user's saved knob values. Optional: without it, saved values are empty.
type Profiles interface {
	SavedKnobs(ctx context.Context, user domain.UserID) (map[string]float64, error)
}

type Service struct {
	schema   *schema.Service
	ranker   Ranker
	profiles Profiles
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

const (
	defaultLimit = 30
	maxLimit     = 200
	maxSeedItems = 1000
)

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

// Recommend runs one request.
func (s *Service) Recommend(ctx context.Context, req Request) (*Response, error) {
	started := time.Now()
	c := s.schema.Compiled()
	if c == nil {
		return nil, fail(409, "schema_missing", "no schema has been pushed yet")
	}
	base := &view{sch: c.Schema, c: c}

	name := req.Recommender
	spec, ok := specFor(base.sch, name)
	if !ok {
		msg := fmt.Sprintf("%q is not a recommender in this schema", name)
		if len(base.sch.Recommenders) > 0 && name == "default" {
			msg = "this schema declares its own recommenders; call one by name"
		}
		return nil, fail(404, "unknown_recommender", "%s", msg)
	}

	seed, serr := validateSeed(name, spec, req)
	if serr != nil {
		return nil, serr
	}
	now := s.now().UTC()
	cx, cerr := checkContext(base.sch, req.Context, now)
	if cerr != nil {
		return nil, cerr
	}
	recID := newID("rec_")

	// Experiment variant and holdout. A holdout user is served the holdout recommender.
	asg, v, holdout := s.assign(base, name, seed.User, recID, cx)
	used := name
	if holdout {
		used = base.sch.Holdout.Recommender
		spec, _ = specFor(base.sch, used)
	}

	weights, meta, knobsOut, clamped, rerr := s.resolve(ctx, v, used, spec, req, seed, cx)
	if rerr != nil {
		return nil, rerr
	}

	// Fallback: the first condition that holds switches to that recommender, once.
	if !holdout {
		for _, f := range spec.Fallback {
			if holds, _ := evalBool(f.When, seed, cx); holds {
				if next, ok := specFor(v.sch, f.Use); ok {
					used, spec = f.Use, next
					weights, meta, knobsOut, clamped, rerr = s.resolve(ctx, v, used, spec, req, seed, cx)
					if rerr != nil {
						return nil, rerr
					}
				}
				break
			}
		}
	}

	limit := req.Limit
	switch {
	case limit <= 0 && spec.Limit != nil:
		limit = spec.Limit.Default
	case limit <= 0:
		limit = defaultLimit
	}
	ceiling := maxLimit
	if spec.Limit != nil && spec.Limit.Max > 0 {
		ceiling = spec.Limit.Max
	}
	limit = min(limit, ceiling)

	exclude := make(map[domain.EntityID]bool, len(req.Exclude))
	for _, id := range req.Exclude {
		exclude[domain.EntityID(id)] = true
	}
	provided := map[string]map[domain.EntityID]float64{}
	for name, scores := range req.Scores {
		m := make(map[domain.EntityID]float64, len(scores))
		for id, sc := range scores {
			if math.IsNaN(sc) || math.IsInf(sc, 0) {
				return nil, fail(400, "validation_error", "scores.%s.%s must be a finite number", name, id)
			}
			m[domain.EntityID(id)] = sc
		}
		provided[name] = m
	}

	out, rankErr := s.ranker.Rank(ctx, RankInput{
		Recommender: used, Spec: spec, Schema: v.sch, Seed: seed, User: seed.User, Context: cx,
		Weights: weights, Meta: meta, Limit: limit, Exclude: exclude, Provided: provided,
	})
	if rankErr != nil {
		return nil, fail(500, "internal", "ranking failed: %v", rankErr)
	}

	scored := make([]domain.ScoredItem, 0, len(out.Items))
	for _, it := range out.Items {
		if !exclude[it.Item] {
			scored = append(scored, it)
		}
	}
	entityType := ""
	if len(spec.For) > 0 {
		entityType = spec.For[0]
	}
	items := make([]Item, 0, min(limit, len(scored)))
	for _, it := range scored[:min(limit, len(scored))] {
		typ := out.Types[it.Item]
		if typ == "" {
			typ = entityType
		}
		item := Item{ID: string(it.Item), Type: typ, Score: it.Score}
		if req.Explain {
			item.Reason = out.Reasons[it.Item]
		}
		items = append(items, item)
	}

	s.results.put(&stored{
		RecID: recID, Recommender: name, Used: used, User: seed.User, Items: scored, Reasons: out.Reasons,
		Weights: weights, Meta: meta, Experiment: asg, Holdout: holdout, At: now,
	})
	return &Response{
		Recommender: name, Used: used, RecID: recID, Items: items, Weights: weights, Knobs: knobsOut,
		Clamped: clamped, Experiment: asg, Holdout: holdout, CandidatesConsidered: out.Candidates,
		TookMS: float64(time.Since(started).Microseconds()) / 1000,
	}, nil
}

// validateSeed checks that the request carries the seed field the recommender's seed type needs,
// and none that belong to another type. `user` is allowed beside any seed: it supplies the saved
// knobs and taste.
func validateSeed(name string, spec schema.RecommenderSpec, req Request) (Seed, *Error) {
	need := spec.EffectiveSeed()
	has := map[string]bool{
		schema.SeedItem:    req.Item != "",
		schema.SeedItems:   req.Items != nil,
		schema.SeedSession: req.Session != nil,
		schema.SeedUsers:   req.Users != nil,
	}
	field := map[string]string{
		schema.SeedUser: "user", schema.SeedItem: "item", schema.SeedItems: "items",
		schema.SeedSession: "session", schema.SeedUsers: "users",
	}
	present := func() string {
		var got []string
		if req.User != "" {
			got = append(got, "user")
		}
		for _, k := range []string{schema.SeedItem, schema.SeedItems, schema.SeedSession, schema.SeedUsers} {
			if has[k] {
				got = append(got, field[k])
			}
		}
		return fmt.Sprint(got)
	}
	mismatch := func(format string, args ...any) (Seed, *Error) {
		return Seed{}, fail(400, "seed_mismatch", format, args...)
	}

	s := Seed{Kind: need, User: domain.UserID(req.User), present: true}
	for k, set := range has {
		if set && k != need {
			return mismatch("%s takes %s, not %s (sent: %s)", name, seedText(need, field), field[k], present())
		}
	}
	switch need {
	case schema.SeedUser:
		if req.User == "" {
			return mismatch("%s needs user", name)
		}
	case schema.SeedItem:
		if req.Item == "" {
			return mismatch("%s needs item, not %s", name, present())
		}
		s.Item = domain.EntityID(req.Item)
	case schema.SeedItems:
		if req.Items == nil {
			return mismatch("%s needs items, not %s", name, present())
		}
		if len(req.Items) > maxSeedItems {
			return Seed{}, fail(400, "validation_error", "items holds %d songs; at most %d", len(req.Items), maxSeedItems)
		}
		s.Items = toEntities(req.Items)
	case schema.SeedSession:
		if req.Session == nil {
			return mismatch("%s needs session, not %s", name, present())
		}
		if len(req.Session) > maxSeedItems {
			return Seed{}, fail(400, "validation_error", "session holds %d items; at most %d", len(req.Session), maxSeedItems)
		}
		s.Items = toEntities(req.Session)
	case schema.SeedUsers:
		if len(req.Users) == 0 {
			return mismatch("%s needs users, a non-empty list", name)
		}
		for _, u := range req.Users {
			s.Users = append(s.Users, domain.UserID(u))
		}
	case schema.SeedNone:
	}
	return s, nil
}

func seedText(kind string, field map[string]string) string {
	if kind == schema.SeedNone {
		return "no seed"
	}
	return field[kind]
}

func toEntities(ids []string) []domain.EntityID {
	out := make([]domain.EntityID, len(ids))
	for i, id := range ids {
		out[i] = domain.EntityID(id)
	}
	return out
}

// resolve turns knobs and weights into the numbers for one recommender: knob defaults, preset,
// the user's saved values and the request's overrides (only knobs the recommender offers), then
// its own `weights`, which apply to a signal no in-scope knob drives.
func (s *Service) resolve(ctx context.Context, v *view, name string, spec schema.RecommenderSpec, req Request,
	seed Seed, cx map[string]domain.Value,
) (weights, meta, knobsOut map[string]float64, clamped []string, _ *Error) {
	var scope []string // nil: every knob
	if len(spec.Knobs) > 0 {
		scope = slices.Clone(spec.Knobs)
	}
	for _, id := range slices.Sorted(keys(req.Knobs)) {
		if _, ok := v.c.KnobIndex[id]; !ok {
			return nil, nil, nil, nil, fail(400, "validation_error", "unknown knob %q", id)
		}
		if scope != nil && !slices.Contains(scope, id) {
			return nil, nil, nil, nil, fail(400, "validation_error", "knob %q is not offered by %s (it offers %v)", id, name, scope)
		}
		if f := req.Knobs[id]; math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, nil, nil, nil, fail(400, "validation_error", "knob %q must be a finite number", id)
		}
	}

	var saved map[string]float64
	if s.profiles != nil && seed.User != "" {
		got, perr := s.profiles.SavedKnobs(ctx, seed.User)
		if perr != nil {
			return nil, nil, nil, nil, fail(500, "internal", "reading saved knobs: %v", perr)
		}
		saved = map[string]float64{}
		for id, val := range got { // a knob removed by a schema change is ignored, not an error
			if _, ok := v.c.KnobIndex[id]; ok {
				saved[id] = val
			}
		}
	}

	res, kerr := knobs.Resolve(v.c, knobs.Input{Preset: req.Preset, Saved: saved, Overrides: req.Knobs, Scope: scope})
	if kerr != nil {
		return nil, nil, nil, nil, fail(400, "validation_error", "%v", kerr)
	}

	// signals a knob in scope drives: a recommender weight must not undo the user's choice
	driven := map[int]bool{}
	for _, k := range v.c.Knobs {
		if scope != nil && !slices.Contains(scope, k.ID) {
			continue
		}
		for _, b := range k.Bindings {
			if b.Signal >= 0 {
				driven[b.Signal] = true
			}
		}
	}

	ids := spec.Signals
	if len(ids) == 0 {
		ids = v.sch.SignalIDs()
	}
	weights = make(map[string]float64, len(ids))
	for _, id := range ids {
		idx, ok := v.c.SignalIndex[id]
		if !ok {
			continue
		}
		w := res.Weights[idx]
		if rw, has := spec.Weights[id]; has && !driven[idx] {
			val, werr := evalNumber(string(rw), seed, cx)
			if werr != nil {
				return nil, nil, nil, nil, fail(500, "internal", "weight of %s in %s: %v", id, name, werr)
			}
			w = val
		}
		weights[id] = w
	}

	meta = make(map[string]float64, len(v.c.MetaNames))
	for i, n := range v.c.MetaNames {
		meta[n] = res.Meta[i]
	}
	knobsOut = make(map[string]float64, len(v.c.Knobs))
	for i, k := range v.c.Knobs {
		if scope == nil || slices.Contains(scope, k.ID) {
			knobsOut[k.ID] = res.Knobs[i]
		}
	}
	return weights, meta, knobsOut, res.Clamped, nil
}

func keys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// env is what request-time expressions may read: seed.size, profile.size, $context.x. $user
// attributes need the store, so they read as null until it exists.
func env(seed Seed, cx map[string]domain.Value) expr.Env {
	e := expr.Env{
		"seed.size":    domain.Num(float64(seed.Size())),
		"profile.size": domain.Num(0), // no store yet: no history
	}
	for name, val := range cx {
		e["$context."+name] = val
	}
	return e
}

func evalNumber(src string, seed Seed, cx map[string]domain.Value) (float64, error) {
	x, err := expr.Compile(src)
	if err != nil {
		return 0, err
	}
	v, err := x.Eval(env(seed, cx))
	if err != nil {
		return 0, err
	}
	f, ok := v.AsFloat()
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%q is not a finite number", src)
	}
	return f, nil
}

func evalBool(src string, seed Seed, cx map[string]domain.Value) (bool, error) {
	x, err := expr.Compile(src)
	if err != nil {
		return false, err
	}
	v, err := x.Eval(env(seed, cx))
	if err != nil {
		return false, err
	}
	b, ok := v.AsBool()
	return ok && b, nil
}

// sortedNames returns map keys in order, for deterministic assignment.
func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
