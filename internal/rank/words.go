package rank

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/timurcravtov/walrus/internal/schema"
)

// say writes a signal's sentence: the schema's own `explain` text, in the request's locale, when it
// has one and every {placeholder} in it can be filled; otherwise the built-in wording, which is
// English.
func (rk *ranking) say(spec schema.SignalSpec, fallback string, vals map[string]string) string {
	own := spec.Explain.Plain
	if spec.Explain.Locales != nil {
		own = spec.Explain.In(rk.in.Locale, rk.sch.DefaultLocale())
	}
	for _, text := range []string{own, fallback} {
		if text == "" {
			continue
		}
		for k, v := range vals {
			text = strings.ReplaceAll(text, "{"+k+"}", v)
		}
		if !strings.Contains(text, "{") {
			return text
		}
	}
	return ""
}

func (s *snapshot) names(items []int) string {
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = s.name(item)
	}
	return strings.Join(names, " and ")
}

// termPhrase puts the similarity term that did the most work for this pair into words, using the
// values the two items actually share. Numbers compared one by one (closeness terms) read as one
// sound, since the signal adds them up.
func (rk *ranking) termPhrase(cand, seedItem int, sel []bool, weights []float64) string {
	a, b := rk.items[cand].Attrs, rk.items[seedItem].Attrs
	name := rk.name(seedItem)

	bestTerm, bestScore := -1, 0.0
	var sound []feature
	soundScore := 0.0
	for k, t := range rk.terms {
		if !sel[k] || weights[k] <= 0 {
			continue
		}
		v, ok := rk.termValue(cand, seedItem, t)
		switch {
		case !ok:
		case t.metric == string(schema.MetricCloseness):
			sound = append(sound, feature{name: humanise(t.id), attr: t.attrs[0]})
			soundScore += weights[k] * v
		case weights[k]*v > bestScore:
			bestTerm, bestScore = k, weights[k]*v
		}
	}
	if soundScore > bestScore {
		if near := rk.closest(cand, seedItem, sound, 2); len(near) > 0 {
			return "Close to " + name + " in " + list(near, 2)
		}
		return "Sounds like " + name
	}
	if bestTerm < 0 {
		return ""
	}
	t := rk.terms[bestTerm]
	switch schema.Metric(t.metric) {
	case schema.MetricJaccard:
		have, _ := a[t.attrs[0]].AsSet()
		other, _ := b[t.attrs[0]].AsSet()
		var shared []string
		for _, v := range have {
			if contains(other, v) {
				shared = append(shared, v)
			}
		}
		if len(shared) > 0 {
			return "Shares " + list(shared, 3) + " with " + name
		}
	case schema.MetricEquals:
		return "Same " + humanise(t.attrs[0]) + " as " + name
	case schema.MetricLogRatio:
		return "Similar " + humanise(t.attrs[0]) + " to " + name
	case schema.MetricCosine:
		if len(t.attrs) == 1 {
			return "Sounds like " + name
		}
		feats := make([]feature, len(t.attrs))
		for i, attr := range t.attrs {
			feats[i] = feature{name: humanise(attr), attr: attr}
		}
		if near := rk.closest(cand, seedItem, feats, 2); len(near) > 0 {
			return "Close to " + name + " in " + list(near, 2)
		}
		return "Sounds like " + name
	}
	return ""
}

// feature is a number to compare, and the word for it.
type feature struct{ name, attr string }

// distinctive is how much nearer than typical a pair must be on an attribute to be worth saying.
const distinctive = 0.5

// closest names the features on which the two items are unusually near: much nearer than two of
// the candidates typically are. A feature that is about the same across all candidates is never
// worth naming. It returns at most n, nearest first.
func (s *snapshot) closest(cand, seedItem int, feats []feature, n int) []string {
	type gap struct {
		name  string
		ratio float64
	}
	var gaps []gap
	for _, f := range feats {
		fa, okA := s.items[cand].Attrs[f.attr].AsFloat()
		fb, okB := s.items[seedItem].Attrs[f.attr].AsFloat()
		typical := s.gaps[f.attr]
		if !okA || !okB || typical == 0 {
			continue
		}
		if ratio := math.Abs(fa-fb) / typical; ratio < distinctive {
			gaps = append(gaps, gap{f.name, ratio})
		}
	}
	sort.SliceStable(gaps, func(i, j int) bool { return gaps[i].ratio < gaps[j].ratio })
	var out []string
	for _, g := range gaps[:min(n, len(gaps))] {
		out = append(out, g.name)
	}
	return out
}

// list joins up to n items as "a, b and c", and says how many more there were.
func list(items []string, n int) string {
	more := 0
	if len(items) > n {
		more, items = len(items)-n, items[:n]
	}
	var text string
	switch len(items) {
	case 0:
		return ""
	case 1:
		text = items[0]
	default:
		text = strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
	if more > 0 {
		text += fmt.Sprintf(" and %d more", more)
	}
	return text
}

func quote(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "“" + s + "”"
	}
	return out
}

// humanise turns an attribute name into words: artist_id into artist, release_date into release date.
func humanise(attr string) string {
	return strings.ReplaceAll(strings.TrimSuffix(attr, "_id"), "_", " ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func num(f float64) string { return fmt.Sprintf("%.2f", f) }
