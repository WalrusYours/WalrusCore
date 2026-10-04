// Package learn trains the models a schema asks for. It is the precompute side of the `embedding`
// signal: it reads the catalogue and the interactions from the store, weights them the way the
// ranker's user profiles do, trains item vectors with package factors, and saves the model back.
// The ranker only ever reads a saved model.
package learn

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/factors"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store"
)

// SignalType is the schema signal type this package trains.
const SignalType = "embedding"

// Signals lists the ids of the schema's embedding signals, in order.
func Signals(sch *schema.Schema) []string {
	var out []string
	for _, id := range sch.SignalIDs() {
		if sch.Signals[id].Type == SignalType {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// ParamsOf reads an embedding signal's training settings; what it leaves out takes the engine's
// default. The schema validator has already checked the values.
func ParamsOf(spec schema.SignalSpec) factors.Params {
	p := factors.DefaultParams()
	if n, ok := number(spec.Params["factors"]); ok {
		p.Factors = int(n)
	}
	if n, ok := number(spec.Params["regularization"]); ok {
		p.Regularization = n
	}
	if n, ok := number(spec.Params["alpha"]); ok {
		p.Alpha = n
	}
	if n, ok := number(spec.Params["iterations"]); ok {
		p.Iterations = int(n)
	}
	return p
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// Entity is the entity type an embedding signal scores and learns: its `for`, else the ranked type.
func Entity(sch *schema.Schema, spec schema.SignalSpec) string {
	if len(spec.For) > 0 {
		return spec.For[0]
	}
	t, _ := sch.Ranked()
	return t
}

// Of is the interaction types an embedding learns from, sorted: the ones its `of` names, else every
// positive interaction that targets the entity.
func Of(sch *schema.Schema, spec schema.SignalSpec) []string {
	var of []string
	if raw, ok := spec.Params["of"].([]any); ok {
		for _, e := range raw {
			if s, isStr := e.(string); isStr {
				of = append(of, s)
			}
		}
	} else {
		entity := Entity(sch, spec)
		ranked, _ := sch.Ranked()
		for name, it := range sch.Interactions {
			target := it.Target
			if target == "" {
				target = ranked
			}
			if it.Positive() && target == entity {
				of = append(of, name)
			}
		}
	}
	slices.Sort(of)
	return of
}

// Result says what one training run did.
type Result struct {
	Signal       string  `json:"signal"`
	Version      int     `json:"version,omitempty"`
	Items        int     `json:"items,omitempty"`
	Users        int     `json:"users,omitempty"`
	Interactions int     `json:"interactions,omitempty"`
	Loss         float64 `json:"loss,omitempty"`
	TookMS       float64 `json:"took_ms"`
	// Error is why nothing was trained; the previous model, if any, keeps serving.
	Error string `json:"error,omitempty"`
}

// Train trains the model of one embedding signal from the store and saves it as the signal's active
// model. If it fails, the model that was active stays active.
func Train(ctx context.Context, st store.Store, c *schema.Compiled, signalID string, now time.Time) (Result, error) {
	start := time.Now()
	res := Result{Signal: signalID}
	spec, ok := c.Schema.Signals[signalID]
	if !ok || spec.Type != SignalType {
		return res, fmt.Errorf("learn: %q is not an embedding signal of this schema", signalID)
	}
	entity := Entity(c.Schema, spec)
	of := Of(c.Schema, spec)

	entities, err := st.ListEntities(ctx, entity)
	if err != nil {
		return res, err
	}
	items := make([]domain.EntityID, len(entities))
	for i, e := range entities {
		items[i] = e.ID
	}
	history, err := st.Interactions(ctx, of...)
	if err != nil {
		return res, err
	}
	users, obs := gather(items, history, c, of, now)

	model, err := factors.Train(items, users, obs, ParamsOf(spec), now)
	if err != nil {
		return res, err
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	model.Of = of
	version, err := st.SaveModel(ctx, signalID, model)
	if err != nil {
		return res, err
	}
	res.Version, res.Items, res.Users, res.Interactions, res.Loss = version, len(items), model.Users, model.Interactions, model.Loss
	res.TookMS = float64(time.Since(start).Microseconds()) / 1000
	return res, nil
}

// TrainAll trains every embedding signal of the schema. A signal that cannot be trained (there is
// nothing to learn from yet, say) is reported in its Result and does not stop the others; only a
// cancelled context returns an error.
func TrainAll(ctx context.Context, st store.Store, c *schema.Compiled, now time.Time) ([]Result, error) {
	return trainEach(ctx, st, c, Signals(c.Schema), now)
}

// trainEach trains the given embedding signals one after another; see TrainAll.
func trainEach(ctx context.Context, st store.Store, c *schema.Compiled, ids []string, now time.Time) ([]Result, error) {
	var out []Result
	for _, id := range ids {
		res, err := Train(ctx, st, c, id, now)
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			res.Error = reason(err)
		}
		out = append(out, res)
	}
	return out, nil
}

func reason(err error) string {
	if errors.Is(err, factors.ErrNoData) {
		return "there are no interactions to learn from yet"
	}
	return err.Error()
}

// gather turns interactions into observations. Each counts with its schema weight times its
// transformed value, fading with its half-life, as in the ranker's user profiles; interactions
// that do not count (negative, wrong type, unknown item, no usable value) are left out. Users are
// numbered in sorted order, so the same data always gives the same observations.
func gather(items []domain.EntityID, history []domain.Interaction, c *schema.Compiled, of []string, now time.Time) (users int, obs []factors.Obs) {
	itemRow := make(map[domain.EntityID]int, len(items))
	for i, id := range items {
		itemRow[id] = i
	}
	seen := map[domain.UserID]bool{}
	for _, it := range history {
		if _, ok := itemRow[it.Target]; ok && slices.Contains(of, it.Type) {
			seen[it.User] = true
		}
	}
	names := make([]domain.UserID, 0, len(seen))
	for u := range seen {
		names = append(names, u)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	userRow := make(map[domain.UserID]int, len(names))
	for i, u := range names {
		userRow[u] = i
	}

	for _, it := range history {
		row, ok := itemRow[it.Target]
		rule, known := c.Interactions[it.Type]
		if !ok || !known || !slices.Contains(of, it.Type) {
			continue
		}
		w, err := rule.EdgeWeight(it.Value)
		if err != nil || !(w > 0) || math.IsInf(w, 0) {
			continue
		}
		if rule.HalfLife > 0 {
			age := max(now.Sub(it.TS), 0)
			w *= math.Exp2(-float64(age) / float64(rule.HalfLife))
		}
		if w > 0 {
			obs = append(obs, factors.Obs{User: userRow[it.User], Item: row, Value: w})
		}
	}
	return len(names), obs
}

// State is what the engine knows about one embedding signal's model, for the operator.
type State struct {
	Signal  string `json:"signal"`
	Entity  string `json:"entity"`
	Trained bool   `json:"trained"`
	Version int    `json:"version,omitempty"`
	// TrainedAt, Settings and the counts describe the active model.
	TrainedAt    *time.Time `json:"trained_at,omitempty"`
	Items        int        `json:"items,omitempty"`
	Users        int        `json:"users,omitempty"`
	Interactions int        `json:"interactions,omitempty"`
	Loss         float64    `json:"loss,omitempty"`
	Settings     *Settings  `json:"settings,omitempty"`
	// Wanted is what the schema asks for now. RetrainPending is true when it differs from the active
	// model (or there is none); the active model keeps serving until the next run replaces it.
	Wanted         Settings `json:"wanted"`
	RetrainPending bool     `json:"retrain_pending"`
	Reason         string   `json:"reason,omitempty"`
}

// Settings are the training settings a schema can change, which decide whether a model is stale.
type Settings struct {
	Factors        int      `json:"factors"`
	Regularization float64  `json:"regularization"`
	Alpha          float64  `json:"alpha"`
	Iterations     int      `json:"iterations"`
	Of             []string `json:"of"`
}

func settings(p factors.Params, of []string) Settings {
	return Settings{Factors: p.Factors, Regularization: p.Regularization, Alpha: p.Alpha, Iterations: p.Iterations, Of: of}
}

// Status reports every embedding signal's model against what the schema asks for now.
func Status(ctx context.Context, st store.Store, c *schema.Compiled) ([]State, error) {
	var out []State
	for _, id := range Signals(c.Schema) {
		spec := c.Schema.Signals[id]
		want := settings(ParamsOf(spec), Of(c.Schema, spec))
		s := State{Signal: id, Entity: Entity(c.Schema, spec), Wanted: want}
		m, err := st.Model(ctx, id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			s.RetrainPending, s.Reason = true, "not trained yet"
		case err != nil:
			return nil, err
		default:
			have := settings(m.Params, m.Of)
			at := m.TrainedAt
			s.Trained, s.Version, s.TrainedAt, s.Settings = true, m.Version, &at, &have
			s.Items, s.Users, s.Interactions, s.Loss = len(m.Items), m.Users, m.Interactions, m.Loss
			if have.Factors != want.Factors || have.Regularization != want.Regularization ||
				have.Alpha != want.Alpha || have.Iterations != want.Iterations {
				s.RetrainPending, s.Reason = true, "the training settings changed since this model was trained"
			} else if !slices.Equal(have.Of, want.Of) {
				s.RetrainPending, s.Reason = true, "the interactions it learns from changed since this model was trained"
			}
		}
		out = append(out, s)
	}
	return out, nil
}
