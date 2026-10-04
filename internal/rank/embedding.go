package rank

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/timurcravtov/walrus/internal/factors"
	"github.com/timurcravtov/walrus/internal/learn"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store"
)

// The embedding signal scores an item by how well its learned vector fits the requesting user. The
// vectors are trained offline (package learn) and read from the store with the snapshot; the user's
// own vector is solved from their history at request time, so it follows the horizon and
// half-life knobs, and a user who did something a minute ago is already reflected.

// embeddingData is what a snapshot keeps for one embedding signal.
type embeddingData struct {
	model   *factors.Model
	row     []int       // snapshot item position to model row; -1 for an item the model has not seen
	rowItem []int       // model row to snapshot item position; -1 for an item no longer in the snapshot
	history []histEntry // the requesting user's positive interactions with items the model knows
}

type histEntry struct {
	row      int
	typ      string
	weight   float64       // the schema weight times the transformed value, before any fade
	halfLife time.Duration // the interaction type's half-life; 0 for none
	age      time.Duration
}

// loadModels reads the trained model of every embedding signal the request can use: the
// recommender's own signals, and the signal a `factors` candidate source names. A signal with no
// trained model is simply left out; it scores every item alike until one is trained.
func (s *snapshot) loadModels(ctx context.Context) error {
	s.models = map[string]*factors.Model{}
	for _, id := range s.embeddingSignals() {
		m, err := s.ranker.store.Model(ctx, id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			s.ranker.noteOnce("no model/"+id, "rank: an embedding signal has no trained model yet; it scores every item alike until one is trained (POST /v1/models/train)", "signal", id)
		case err != nil:
			return err
		default:
			s.models[id] = m
		}
	}
	return nil
}

// embeddingSignals are the ids of the embedding signals this request uses.
func (s *snapshot) embeddingSignals() []string {
	var ids []string
	seen := map[string]bool{}
	add := func(id string) {
		if sp, ok := s.sch.Signals[id]; ok && sp.Type == learn.SignalType && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, id := range s.signalIDs() {
		add(id)
	}
	for _, src := range s.spec.Candidates {
		if src.Source == "factors" {
			add(src.Signal)
		}
	}
	return ids
}

// prepareEmbedding maps the snapshot's items onto the model's rows and collects the requesting
// user's history. It returns nil when there is no usable model, which the signal treats as constant.
func prepareEmbedding(s *snapshot, id string, spec schema.SignalSpec) any {
	m := s.models[id]
	if m == nil || learn.Entity(s.sch, spec) != s.typ {
		return nil
	}
	d := &embeddingData{model: m, row: make([]int, len(s.items)), rowItem: make([]int, len(m.Items))}
	for r := range d.rowItem {
		d.rowItem[r] = -1
	}
	for i, e := range s.items {
		d.row[i] = -1
		if r, ok := m.Row(e.ID); ok {
			d.row[i], d.rowItem[r] = r, i
		}
	}
	if s.user == "" {
		return d
	}
	counted := map[string]bool{}
	for _, name := range learn.Of(s.sch, spec) {
		counted[name] = true
	}
	for _, it := range s.interactions {
		if it.User != s.user || !counted[it.Type] {
			continue
		}
		pos, ok := s.index[it.Target]
		rule, known := s.c.Interactions[it.Type]
		if !ok || !known || d.row[pos] < 0 {
			continue
		}
		w, err := rule.EdgeWeight(it.Value)
		if err != nil || !(w > 0) || math.IsInf(w, 0) {
			continue
		}
		d.history = append(d.history, histEntry{
			row: d.row[pos], typ: it.Type, weight: w, halfLife: rule.HalfLife, age: max(s.now.Sub(it.TS), 0),
		})
	}
	return d
}

// fit solves the user's vector from their history. meta holds the request's resolved
// meta-parameters (the half-life and weight scales a knob can move); nil means the schema's own.
func (d *embeddingData) fit(meta map[string]float64) *factors.Fit {
	entries := make([]factors.Entry, 0, len(d.history))
	for _, h := range d.history {
		w := h.weight * metaScale(meta, "interactions."+h.typ+".weight_scale")
		if h.halfLife > 0 {
			half := float64(h.halfLife) * metaScale(meta, "interactions.half_life_scale") * metaScale(meta, "interactions."+h.typ+".half_life_scale")
			w *= math.Exp2(-float64(h.age) / half)
		}
		entries = append(entries, factors.Entry{Row: h.row, Value: w})
	}
	fit, err := d.model.FoldIn(entries)
	if err != nil { // only a degenerate model; score everything alike rather than fail the request
		fit, _ = d.model.FoldIn(nil)
	}
	return fit
}

// metaScale is a scale meta-parameter, 1 (no change) when the request sets none.
func metaScale(meta map[string]float64, name string) float64 {
	if v, ok := meta[name]; ok {
		return max(v, 1e-6)
	}
	return 1
}

// embedding scores each candidate by the dot product of its vector with the user's, or, for an item
// seed, by its cosine with the seed items. An item the model has not seen scores NaN, which
// normalisation treats as no information.
func embedding(rk *ranking, id string, spec schema.SignalSpec) ([]float64, func(int) string) {
	raw := make([]float64, len(rk.candidates))
	d, ok := rk.prepared[id].(*embeddingData)
	if !ok || d == nil {
		return raw, nil
	}

	switch seed := rk.spec.EffectiveSeed(); seed {
	case schema.SeedUser:
		fit := d.fit(rk.in.Meta)
		for c, i := range rk.candidates {
			if d.row[i] < 0 {
				raw[c] = nan
				continue
			}
			raw[c] = fit.Score(d.row[i])
		}
		return raw, func(c int) string {
			row := d.row[rk.candidates[c]]
			if row < 0 {
				return rk.say(spec, "Too new for the model to know yet", nil)
			}
			var picked []string
			for _, con := range fit.Contributions(row) {
				if item := d.rowItem[con.Row]; con.Value > 0 && con.Row != row && item >= 0 {
					picked = append(picked, rk.name(item))
				}
				if len(picked) == 3 {
					break
				}
			}
			if len(picked) == 0 {
				return ""
			}
			return rk.say(spec, "Because you interacted with {because}", map[string]string{"because": list(quote(picked), 3)})
		}
	case schema.SeedItem, schema.SeedItems, schema.SeedSession:
		raw, best := rk.bySeed(func(cand, k int) float64 {
			a, b := d.row[cand], d.row[rk.seed[k]]
			if a < 0 || b < 0 {
				return nan
			}
			return d.model.Cosine(a, b)
		})
		return raw, func(c int) string {
			if d.row[rk.candidates[c]] < 0 {
				return rk.say(spec, "Too new for the model to know yet", nil)
			}
			seeds := best(c)
			if len(seeds) == 0 {
				return ""
			}
			return rk.say(spec, "People who picked {seed_items} often pick this too", map[string]string{"seed_items": rk.names(seeds)})
		}
	}
	return raw, nil // a seed with no history to fit (none, users): every item scores the same
}

// factorCandidates are the items whose vectors fit the user best, or sit closest to the seed items.
// It reads only the model and the history, never weights or knobs, so the candidates stay the same
// when a slider moves.
func (s *snapshot) factorCandidates(src schema.CandidateSource, cap int) []int {
	d, ok := s.prepared[src.Signal].(*embeddingData)
	if !ok || d == nil {
		return nil // no trained model: loadModels has already said so
	}
	switch s.spec.EffectiveSeed() {
	case schema.SeedUser:
		rows := d.fit(nil).Top(cap, func(row int) bool {
			item := d.rowItem[row]
			return item < 0 || s.inSeed[item]
		})
		out := make([]int, len(rows))
		for k, row := range rows {
			out[k] = d.rowItem[row]
		}
		return out
	case schema.SeedItem, schema.SeedItems, schema.SeedSession:
		best := make([]float64, len(s.items))
		for _, seedItem := range s.seed {
			from := d.row[seedItem]
			if from < 0 {
				continue
			}
			for i := range s.items {
				if to := d.row[i]; to >= 0 && !s.inSeed[i] {
					best[i] = max(best[i], d.model.Cosine(from, to))
				}
			}
		}
		return topIndices(best, cap, s.inSeed)
	}
	return nil
}
