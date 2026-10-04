package rank

import (
	"fmt"
	"maps"
	"slices"

	"github.com/timurcravtov/walrus/internal/schema"
)

// Unsupported lists what a valid schema asks for that this ranker does not do yet. The schema is
// still accepted, since the language allows it, but the platform is told up front instead of
// finding out from a log line.
func (r *Ranker) Unsupported(sch *schema.Schema) []schema.Issue {
	var out []schema.Issue
	add := func(path, format string, args ...any) {
		out = append(out, schema.Issue{Path: path, Message: fmt.Sprintf(format, args...)})
	}

	for _, id := range sch.SignalIDs() {
		if t := sch.Signals[id].Type; signalTypes[t].score == nil {
			add("signals."+id+".type", "signal type %s is accepted but not ranked yet; it is skipped", t)
		}
	}
	for _, name := range sortedNames(sch.Recommenders) {
		rec := sch.Recommenders[name]
		p := "recommenders." + name
		for i, src := range rec.Candidates {
			if _, ok := sources[src.Source]; !ok {
				add(fmt.Sprintf("%s.candidates[%d].source", p, i), "candidate source %s is not built yet; it adds no candidates", src.Source)
			}
		}
		if rec.Rerank != nil {
			add(p+".rerank", "re-ranking is not built yet; the list keeps its score order")
		}
		if rec.Mix != nil {
			add(p+".mix", "mixing is not built yet")
		}
		if rec.Group != nil {
			add(p+".group", "group recommendations are not built yet")
		}
		if rec.Reciprocal != nil {
			add(p+".reciprocal", "reciprocal matching is not built yet")
		}
	}
	for i, rule := range sch.Rules {
		if _, ok := rules[ruleKind(rule)]; !ok {
			add(fmt.Sprintf("rules[%d]", i), "rule %s (%s) is not built yet; it is skipped", rule.ID, ruleKind(rule))
		}
	}
	return out
}

func sortedNames[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }
