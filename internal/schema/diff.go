package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type Verdict string

const (
	VerdictNone     Verdict = "none"
	VerdictAdditive Verdict = "additive"
	VerdictBreaking Verdict = "breaking"
)

type DiffResult struct {
	Verdict Verdict  `json:"verdict"`
	Changes []string `json:"changes"`
}

var (
	// Removing a recommender is breaking too: hosts call it by name.
	breakingGroup = regexp.MustCompile(`^(entities\.[^.]+\.attributes\.[^.]+|entities\.[^.]+|interactions\.[^.]+|signals\.[^.]+|recommenders\.[^.]+)`)
	softGroup     = regexp.MustCompile(`^(knobs\.[^.]+|presets\.[^.]+|similarity\.[^.]+)`)
)

// Diff compares two schemas. Removing an entity, attribute, interaction or signal, or
// changing an attribute or signal type, is breaking: stored data or graphs must be rebuilt.
// Everything else (new keys, changed weights, labels, knobs, presets) is additive and
// applies live. A nil old schema is the first push.
func Diff(oldS, newS *Schema) DiffResult {
	a, b := map[string]string{}, map[string]string{}
	if oldS != nil {
		flatten(generic(oldS), "", a)
	}
	flatten(generic(newS), "", b)

	var changes []string
	breaking := false
	for _, k := range sortedKeys(b) {
		switch old, had := a[k]; {
		case !had:
			changes = append(changes, fmt.Sprintf("+ %s = %s", k, b[k]))
		case old != b[k]:
			changes = append(changes, fmt.Sprintf("~ %s: %s → %s", k, old, b[k]))
			if strings.HasSuffix(k, ".type") && (strings.HasPrefix(k, "signals.") || strings.Contains(k, ".attributes.")) {
				breaking = true
			}
		}
	}
	gone := map[string]bool{}
	for _, k := range sortedKeys(a) {
		if _, kept := b[k]; kept {
			continue
		}
		for _, re := range []*regexp.Regexp{breakingGroup, softGroup} {
			g := re.FindString(k)
			if g == "" {
				continue
			}
			if stillThere(b, g) {
				break
			}
			if !gone[g] {
				changes = append(changes, "- "+g)
				gone[g] = true
			}
			if re == breakingGroup {
				breaking = true
			}
			goto next
		}
		changes = append(changes, "- "+k)
	next:
	}
	switch {
	case len(changes) == 0:
		return DiffResult{Verdict: VerdictNone, Changes: []string{}}
	case breaking:
		return DiffResult{Verdict: VerdictBreaking, Changes: changes}
	}
	return DiffResult{Verdict: VerdictAdditive, Changes: changes}
}

func stillThere(m map[string]string, group string) bool {
	for k := range m {
		if k == group || strings.HasPrefix(k, group+".") {
			return true
		}
	}
	return false
}

// Hash returns the sha256 of the schema's canonical form, so formatting and key order do
// not change it.
func Hash(s *Schema) string {
	b, _ := json.Marshal(generic(s))
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// generic converts a Schema to plain maps. Knobs are keyed by id so reordering them is not
// a change.
func generic(s *Schema) map[string]any {
	raw, _ := yaml.Marshal(s)
	var m map[string]any
	_ = yaml.Unmarshal(raw, &m)
	if list, ok := m["knobs"].([]any); ok {
		byID := map[string]any{}
		for _, k := range list {
			if km, ok := k.(map[string]any); ok {
				id := fmt.Sprint(km["id"])
				delete(km, "id")
				byID[id] = km
			}
		}
		m["knobs"] = byID
	}
	return m
}

func flatten(v any, prefix string, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(mapsKeys(x)) {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flatten(x[k], p, out)
		}
	case []any:
		for i, e := range x {
			flatten(e, fmt.Sprintf("%s[%d]", prefix, i), out)
		}
	default:
		b, _ := json.Marshal(x)
		out[prefix] = string(b)
	}
}
