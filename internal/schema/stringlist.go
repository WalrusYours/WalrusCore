package schema

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// StringList accepts either one string or a list of strings in YAML.
type StringList []string

func (l *StringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var s string
		if err := n.Decode(&s); err != nil {
			return err
		}
		*l = StringList{s}
	case yaml.SequenceNode:
		var ss []string
		if err := n.Decode(&ss); err != nil {
			return err
		}
		*l = ss
	default:
		return fmt.Errorf("line %d: expected a string or a list of strings", n.Line)
	}
	return nil
}

// MarshalYAML writes a single name as a plain string, so a v1 schema's canonical form (and
// its hash) is unchanged by StringList.
func (l StringList) MarshalYAML() (any, error) {
	if len(l) == 1 {
		return l[0], nil
	}
	return []string(l), nil
}
