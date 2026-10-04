package api

import (
	"net/http"
	"slices"

	"github.com/timurcravtov/walrus/internal/knobs"
	"github.com/timurcravtov/walrus/internal/schema"
)

func (s *Server) routeKnobs() {
	s.mux.HandleFunc("GET /v1/schema/knobs", s.requireAdmin(s.schemaKnobs))
}

type knobView struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind"`
	Label     string       `json:"label"`
	Low       string       `json:"low,omitempty"`
	High      string       `json:"high,omitempty"`
	Group     string       `json:"group,omitempty"`
	Help      string       `json:"help,omitempty"`
	Min       float64      `json:"min"`
	Max       float64      `json:"max"`
	Default   float64      `json:"default"`
	DependsOn string       `json:"depends_on,omitempty"`
	Options   []optionView `json:"options,omitempty"`
	// Recommenders are the recommenders that offer this knob.
	Recommenders []string `json:"recommenders"`
}

type optionView struct {
	Value float64 `json:"value"`
	Label string  `json:"label"`
}

type presetView struct {
	ID    string             `json:"id"`
	Knobs map[string]float64 `json:"knobs"`
}

// schemaKnobs describes the knobs and presets of the active schema so a host can draw its Tune
// panel from them. Texts come in the requested locale, falling back to the schema's own.
func (s *Server) schemaKnobs(w http.ResponseWriter, r *http.Request) {
	c := s.schema.Compiled()
	if c == nil {
		writeError(w, http.StatusConflict, "schema_missing", "no schema has been pushed yet")
		return
	}
	locale := r.URL.Query().Get("locale")
	res, err := knobs.Resolve(c, knobs.Input{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not resolve the knob defaults")
		return
	}

	views := make([]knobView, len(c.Knobs))
	for i, k := range c.Knobs {
		v := knobView{
			ID: k.ID, Kind: k.Kind, Label: text(k.Spec.Label, locale), Low: text(k.Spec.Low, locale),
			High: text(k.Spec.High, locale), Group: text(k.Spec.Group, locale), Help: text(k.Spec.Help, locale),
			Min: k.Min, Max: k.Max, Default: res.Knobs[i], DependsOn: k.Spec.DependsOn,
			Recommenders: offeredBy(c.Schema, k.Spec),
		}
		for _, o := range k.Spec.Options {
			v.Options = append(v.Options, optionView{Value: o.Value, Label: text(o.Label, locale)})
		}
		views[i] = v
	}

	var presets []presetView
	for _, id := range sortedKeys(c.Schema.Presets) {
		presets = append(presets, presetView{ID: id, Knobs: c.Schema.Presets[id]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"knobs": views, "presets": presets})
}

// offeredBy lists the recommenders a knob applies to: those that list it, or every recommender
// that lists no knobs at all, narrowed by the knob's own scope. A schema with no recommenders has
// the implicit `default` one.
func offeredBy(sch *schema.Schema, k schema.KnobSpec) []string {
	var out []string
	for _, name := range sortedKeys(sch.Recommenders) {
		spec := sch.Recommenders[name]
		if len(spec.Knobs) > 0 && !slices.Contains(spec.Knobs, k.ID) {
			continue
		}
		if len(k.Scope) > 0 && !slices.Contains(k.Scope, name) {
			continue
		}
		out = append(out, name)
	}
	if len(sch.Recommenders) == 0 {
		out = []string{"default"}
	}
	return out
}

func text(t schema.Text, locale string) string {
	if s, ok := t.Locales[locale]; ok && locale != "" {
		return s
	}
	if t.Plain != "" {
		return t.Plain
	}
	if s, ok := t.Locales["en"]; ok {
		return s
	}
	for _, k := range sortedKeys(t.Locales) {
		return t.Locales[k]
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
