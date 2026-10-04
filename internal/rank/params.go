package rank

import (
	"slices"
	"time"

	"github.com/timurcravtov/walrus/internal/schema"
)

// Signal parameters arrive from YAML as plain values; these read them.

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strList(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func intParam(v any) int {
	n, _ := v.(int)
	return n
}

func dur(v any) (time.Duration, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	d, err := schema.ParseDuration(s)
	return d.Std(), err == nil
}

func contains(list []string, s string) bool { return slices.Contains(list, s) }
