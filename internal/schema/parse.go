package schema

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"slices"

	"gopkg.in/yaml.v3"
)

// Parse decodes schema YAML and rejects unknown keys, so a typo cannot silently drop a rule.
// It checks shape only; semantic validation is a separate step.
func Parse(data []byte) (*Schema, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var s Schema
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("schema is empty")
		}
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	return &s, nil
}

func (s *Schema) SignalIDs() []string { return sortedKeys(s.Signals) }

func (s *Schema) EntityTypes() []string { return sortedKeys(s.Entities) }

func (s *Schema) InteractionTypes() []string { return sortedKeys(s.Interactions) }

func (s *Schema) Knob(id string) (KnobSpec, bool) {
	for _, k := range s.Knobs {
		if k.ID == id {
			return k, true
		}
	}
	return KnobSpec{}, false
}

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

func mapsKeys[V any](m map[string]V) iter.Seq[string] { return maps.Keys(m) }
