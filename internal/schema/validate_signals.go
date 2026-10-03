package schema

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// signalParams checks each type's own keys (SCHEMA-V2.md 4.6). Unknown keys are reported by
// signals(); this checks values and references.
func (v *validator) signalParams(p string, sg SignalSpec) {
	ents := v.signalEntities(sg)
	params := sg.Params
	str := func(key string) (string, bool) {
		raw, ok := params[key]
		if !ok {
			return "", false
		}
		s, isStr := raw.(string)
		if !isStr {
			v.add(p+"."+key, "%s must be a string", key)
			return "", false
		}
		return s, true
	}
	names := func(key string, required bool, check func(string, InteractionSpec) string) []string {
		raw, ok := params[key]
		if !ok {
			if required {
				v.add(p+"."+key, "%s is required: the interactions that count", key)
			}
			return nil
		}
		list, isList := listParam(raw)
		if !isList || len(list) == 0 {
			v.add(p+"."+key, "%s must be an interaction name or a list of them", key)
			return nil
		}
		v.interactionNames(p+"."+key, list, check)
		return list
	}
	positive := func(_ string, spec InteractionSpec) string {
		if !spec.Positive() {
			return "only positive interactions count here"
		}
		return ""
	}
	fulfilling := func(_ string, spec InteractionSpec) string {
		if spec.Fulfils == nil {
			return "add fulfils: true to the interaction; only interactions that fulfil a need count here"
		}
		return ""
	}
	intParam := func(key string, min int) {
		if raw, ok := params[key]; ok {
			if n, isNum := trendNumber(raw); !isNum || n < float64(min) || n != math.Trunc(n) {
				v.add(p+"."+key, "%s must be a whole number, %d or more", key, min)
			}
		}
	}
	// attr checks `on` (or another key naming an attribute) on every scored entity.
	attr := func(key string, required bool, typeCheck func(AttributeSpec) string) {
		on, ok := str(key)
		if !ok {
			if _, present := params[key]; !present && required {
				v.add(p+"."+key, "%s needs %s: <attribute>", sg.Type, key)
			}
			return
		}
		var specs []AttributeSpec
		if len(ents) == 1 && ents[0] == v.ranked && len(sg.For) == 0 {
			if a, found := v.rankedAttr(p+"."+key, on); found {
				specs = []AttributeSpec{a}
			}
		} else {
			specs, _ = v.attrIn(p+"."+key, ents, on)
		}
		if typeCheck != nil {
			for _, a := range specs {
				if msg := typeCheck(a); msg != "" {
					v.add(p+"."+key, "%s", msg)
					break
				}
			}
		}
	}
	numeric := func(a AttributeSpec) string {
		if a.Type != TypeFloat && a.Type != TypeInt {
			return fmt.Sprintf("%s needs a numeric attribute, this one is %s", sg.Type, a.Type)
		}
		return ""
	}

	_, _ = v.durationParam(p, params, "window", false) // validated for every type that has one
	switch sg.Type {
	case "item_neighbors":
		if raw, ok := params["terms"]; ok {
			list, isList := listParam(raw)
			if !isList {
				v.add(p+".terms", "terms must be a term id or a list of them")
			}
			for _, id := range list {
				for _, e := range ents {
					if !v.hasTerm(e, id) {
						v.add(p+".terms", "%q is not a similarity term of %s", id, e)
					}
				}
			}
		}
	case "user_neighbors":
		names("of", false, positive)
		intParam("min_neighbors", 1)
	case "own_history":
		names("of", false, nil)
	case "global_count":
		names("of", false, positive)
	case "trend":
		v.trend(p, sg, ents)
	case "age_decay":
		if _, ok := params["on"]; ok {
			attr("on", true, func(a AttributeSpec) string {
				if a.Type != TypeTimestamp {
					return fmt.Sprintf("age_decay needs a timestamp attribute, %q is %s", params["on"], a.Type)
				}
				return ""
			})
		} else if !v.allHaveCreated(ents) {
			v.add(p+".on", "age_decay needs on: <attribute> (or lifecycle.created on the entity)")
		}
		v.durationParam(p, params, "half_life", false)
	case "low_exposure":
		names("of", false, func(_ string, spec InteractionSpec) string {
			if spec.EffectiveKind() != KindExposure {
				return "low_exposure counts exposures; use an interaction with kind: exposure"
			}
			return ""
		})
	case "attribute_match":
		attr("on", true, nil)
		against, _ := params["against"].(string)
		if against == "" {
			v.add(p+".against", `attribute_match needs against: "$user.<attribute>"`)
		} else {
			v.userRefs(p+".against", against)
		}
	case "diversity_rerank":
		attr("on", true, nil)
	case "co_occurrence":
		of := names("of", true, positive)
		if g, ok := str("group_by"); ok && g != "user" {
			for _, name := range of {
				if spec, declared := v.s.Interactions[name]; declared {
					if _, has := spec.Fields[g]; !has {
						v.add(p+".group_by", "group_by is user or a field every counted interaction has; %s has no field %q", name, g)
					}
				}
			}
		}
		if o, ok := str("order"); ok && o != "any" && o != "after" {
			v.add(p+".order", "order must be any or after")
		}
		intParam("min_support", 1)
		if m, ok := str("measure"); ok && !slices.Contains([]string{"lift", "cosine", "count"}, m) {
			v.add(p+".measure", "measure must be lift, cosine or count")
		}
	case "sequence":
		names("of", true, func(_ string, spec InteractionSpec) string {
			if !spec.Session {
				return "sequence needs interactions with session: true (they carry an order)"
			}
			return ""
		})
		v.durationParam(p, params, "gap", false)
	case "mutual_connections":
		via, ok := str("via")
		switch {
		case !ok:
			if _, present := params["via"]; !present {
				v.add(p+".via", "mutual_connections needs via: <an interaction between users>")
			}
		default:
			spec, declared := v.s.Interactions[via]
			if !declared {
				v.add(p+".via", "%q is not a declared interaction", via)
			} else if spec.Target != "user" {
				v.add(p+".via", "%q must target user to connect people", via)
			}
		}
	case "attribute_target":
		attr("on", true, func(a AttributeSpec) string {
			if a.Type != TypeFloat && a.Type != TypeInt || a.Range == nil {
				return "attribute_target needs a numeric attribute with a range (to measure closeness)"
			}
			return ""
		})
		if t, ok := str("target"); ok {
			switch {
			case t == "knob", t == "seed.mean", t == "seed.median":
			case strings.HasPrefix(t, "$context."):
				v.contextNumeric(p+".target", strings.TrimPrefix(t, "$context."))
			default:
				v.add(p+".target", "target must be knob, seed.mean, seed.median or $context.<field>")
			}
		}
	case "attribute_value":
		attr("on", true, numeric)
	case "context_match":
		attr("on", true, nil)
		against, _ := params["against"].(string)
		if !strings.HasPrefix(against, "$context.") {
			v.add(p+".against", "context_match needs against: $context.<field>")
		} else if _, ok := v.s.Context[strings.TrimPrefix(against, "$context.")]; !ok {
			v.add(p+".against", "%s is not a declared context field", against)
		}
	case "provided":
		if name, ok := str("name"); !ok {
			if _, present := params["name"]; !present {
				v.add(p+".name", "provided needs name: <the score map the host sends>")
			}
		} else {
			v.ident(p+".name", name)
		}
	case "formula":
		src, ok := str("expr")
		if !ok {
			if _, present := params["expr"]; !present {
				v.add(p+".expr", "formula needs expr: <an expression over $item, $user, $context>")
			}
			return
		}
		v.exprRefs(p+".expr", src, ents, refItem|refUser|refContext)
	case "satiation":
		names("of", true, fulfilling)
		attr("by", true, nil)
		if raw, ok := params["level"]; ok {
			intParam("level", 0)
			if n, _ := trendNumber(raw); n > 0 {
				by, _ := params["by"].(string)
				for _, e := range ents {
					a := v.s.Entities[e].Attributes[by]
					if a.Type != TypeRef || v.s.Entities[a.Entity].Parent == "" {
						v.add(p+".level", "level above 0 needs by to be a ref to an entity with a parent")
						break
					}
				}
			}
		}
	case "recurrence":
		names("of", true, fulfilling)
		if v.s.Recurrence == nil {
			v.add(p, "a recurrence signal needs the recurrence section")
		}
	}
}

func (v *validator) hasTerm(entity, id string) bool {
	for _, t := range v.s.Similarity[entity] {
		if len(t.On) > 0 && t.TermID() == id {
			return true
		}
	}
	return false
}

func (v *validator) allHaveCreated(ents []string) bool {
	if len(ents) == 0 {
		return false
	}
	for _, e := range ents {
		lc := v.s.Entities[e].Lifecycle
		if lc == nil || lc.Created == "" {
			return false
		}
	}
	return true
}

func (v *validator) contextNumeric(p, field string) {
	f, ok := v.s.Context[field]
	switch {
	case !ok:
		v.add(p, "$context.%s is not a declared context field", field)
	case f.Type != TypeFloat && f.Type != TypeInt:
		v.add(p, "$context.%s must be numeric, it is %s", field, f.Type)
	}
}

// trend checks a `trend` signal: recent engagement against what is ordinary (ALGORITHMS.md
// 6.1).
func (v *validator) trend(p string, sg SignalSpec, ents []string) {
	against := "auto"
	if raw, ok := sg.Params["against"]; ok {
		s, _ := raw.(string)
		if !slices.Contains([]string{"own", "same_age", "auto"}, s) {
			v.add(p+".against", "against must be own, same_age or auto")
		} else {
			against = s
		}
	}

	window, hasWindow := trendDuration(sg.Params["window"])
	if _, ok := sg.Params["window"]; !ok {
		v.add(p+".window", "a trend needs window: <duration>, the recent period it measures")
	}

	if against != "same_age" {
		raw, ok := sg.Params["baseline"]
		d, valid := trendDuration(raw)
		switch {
		case !ok:
			v.add(p+".baseline", "against: %s needs baseline: <duration>, the period that defines ordinary (or use against: same_age)", against)
		case !valid:
			v.add(p+".baseline", "use a positive duration such as 3d, 12h, 2w")
		case hasWindow && d <= window:
			v.add(p+".baseline", "baseline must be longer than window")
		}
	}

	if raw, ok := sg.Params["on"]; ok {
		on := fmt.Sprint(raw)
		specs, _ := v.attrIn(p+".on", ents, on)
		if against != "own" {
			for _, a := range specs {
				if a.Type != TypeTimestamp {
					v.add(p+".on", "a trend needs a timestamp attribute, %q is %s", on, a.Type)
					break
				}
			}
		}
	} else if against != "own" && !v.allHaveCreated(ents) {
		v.add(p+".on", "against: %s needs on: <timestamp attribute>, which gives an item's age", against)
	}

	if raw, ok := sg.Params["count"]; ok {
		if s, _ := raw.(string); s != "people" && s != "events" {
			v.add(p+".count", "count must be people or events")
		}
	}
	if raw, ok := sg.Params["min"]; ok {
		if n, isNum := trendNumber(raw); !isNum || n < 0 || n != math.Trunc(n) {
			v.add(p+".min", "min must be a whole number, 0 or more")
		}
	}
	if raw, ok := sg.Params["ratio"]; ok {
		if n, isNum := trendNumber(raw); !isNum || n <= 1 {
			v.add(p+".ratio", "ratio must be a number above 1 (how many times ordinary counts as trending)")
		}
	}
	if raw, ok := sg.Params["of"]; ok {
		list, isList := raw.([]any)
		if !isList || len(list) == 0 {
			v.add(p+".of", "of must be a list of interaction names")
		}
		for i, item := range list {
			name, _ := item.(string)
			spec, declared := v.s.Interactions[name]
			switch {
			case !declared:
				v.add(fmt.Sprintf("%s.of[%d]", p, i), "%q is not a declared interaction", fmt.Sprint(item))
			case !spec.Positive():
				v.add(fmt.Sprintf("%s.of[%d]", p, i), "%q has a negative or zero weight; only positive interactions can make a trend", name)
			}
		}
	}
}

// trendDuration reads a duration parameter: valid only if it parses and is positive.
func trendDuration(raw any) (Duration, bool) {
	if raw == nil {
		return 0, false
	}
	d, err := ParseDuration(fmt.Sprint(raw))
	return d, err == nil && d > 0
}

func trendNumber(raw any) (float64, bool) {
	switch n := raw.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	}
	return 0, false
}
