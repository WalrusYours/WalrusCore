package schema

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Tier says what changing a schema path costs, and so who may change it (SCHEMA-V2.md 1,
// Appendix B).
type Tier string

const (
	TierT0      Tier = "T0 (stored data)"
	TierT1      Tier = "T1 (precompute)"
	TierT2      Tier = "T2 (candidates)"
	TierT3      Tier = "T3 (scoring)"
	TierNever   Tier = "not changeable by a variant"
	TierUnknown Tier = "unknown"
)

var tierRules = []struct {
	re   *regexp.Regexp
	tier Tier
}{
	{regexp.MustCompile(`^(entities|recommendable)(\.|$)`), TierT0},
	{regexp.MustCompile(`^interactions\.[^.]+\.half_life$`), TierT3},
	{regexp.MustCompile(`^interactions(\.|$)`), TierT0},
	{regexp.MustCompile(`^similarity(\.|$)`), TierT1},
	{regexp.MustCompile(`^signals\.[^.]+\.(default|normalise|transform|cap|label|explain|from)(\.|$)`), TierT3},
	{regexp.MustCompile(`^signals\.[^.]+\.for$`), TierT2},
	{regexp.MustCompile(`^signals\.[^.]+\.(terms|min_neighbors|target|against|expr|name|by|level|half_life)$`), TierT3},
	{regexp.MustCompile(`^signals\.[^.]+\.`), TierT1},
	{regexp.MustCompile(`^constraints\.[^.]+`), TierT2},
	{regexp.MustCompile(`^(rules|knobs|presets)\.[^.]+`), TierT3},
	{regexp.MustCompile(`^recommenders\.[^.]+\.(weights|signals|rules|rerank|mix|knobs|blend_user|seed_aggregate|label)(\.|$)`), TierT3},
	{regexp.MustCompile(`^recommenders\.[^.]+\.`), TierT2},
	{regexp.MustCompile(`^feedback(\.|$)`), TierT2},
	{regexp.MustCompile(`^recurrence(\.|$)`), TierT1},
	{regexp.MustCompile(`^(meta|context|metrics|experiments|holdout|evaluation|privacy|version)(\.|$)`), TierNever},
}

// PathTier returns the tier of a dotted schema path such as recommenders.home.weights.trend.
func PathTier(path string) Tier {
	for _, r := range tierRules {
		if r.re.MatchString(path) {
			return r.tier
		}
	}
	return TierUnknown
}

// listsByID are the top-level lists whose items a path addresses by id.
var listsByID = []string{"constraints", "rules", "knobs"}

// ApplyPatch returns a copy of s with every path in set changed to its value; a nil value
// removes the key. List items are addressed by id, and setting an id that does not exist adds
// the item. Experiments and the holdout are dropped from the copy: a variant cannot change
// them, and validating the copy must not recurse into them.
func ApplyPatch(s *Schema, set map[string]any) (*Schema, error) {
	raw, err := yaml.Marshal(s)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	delete(doc, "experiments")
	delete(doc, "holdout")
	for _, path := range sortedKeys(set) {
		segs := strings.Split(path, ".")
		next, err := patchAt(doc, segs, set[path], segs[0])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		doc = next.(map[string]any)
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	patched, err := Parse(out)
	if err != nil {
		return nil, fmt.Errorf("the patched schema does not parse: %w", err)
	}
	return patched, nil
}

func patchAt(node any, segs []string, val any, top string) (any, error) {
	key, rest := segs[0], segs[1:]
	switch n := node.(type) {
	case map[string]any:
		if len(rest) == 0 {
			if val == nil {
				delete(n, key)
			} else {
				n[key] = val
			}
			return n, nil
		}
		child, ok := n[key]
		if !ok {
			child = map[string]any{}
		}
		updated, err := patchAt(child, rest, val, top)
		if err != nil {
			return nil, err
		}
		n[key] = updated
		return n, nil
	case []any:
		if !slices.Contains(listsByID, top) {
			return nil, fmt.Errorf("%s is a list; only constraints, rules and knobs are addressed by id", top)
		}
		for i, item := range n {
			m, ok := item.(map[string]any)
			if !ok || fmt.Sprint(m["id"]) != key {
				continue
			}
			if len(rest) == 0 {
				if val == nil {
					return slices.Delete(n, i, i+1), nil
				}
				replacement, ok := val.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("a %s item is a mapping", top)
				}
				replacement["id"] = key
				n[i] = replacement
				return n, nil
			}
			updated, err := patchAt(m, rest, val, top)
			if err != nil {
				return nil, err
			}
			n[i] = updated
			return n, nil
		}
		if len(rest) == 0 {
			if m, ok := val.(map[string]any); ok {
				m["id"] = key
				return append(n, m), nil
			}
		}
		return nil, fmt.Errorf("no %s item with id %q", top, key)
	case nil:
		return patchAt(map[string]any{}, segs, val, top)
	}
	return nil, fmt.Errorf("%q is not a section that can hold keys", key)
}
