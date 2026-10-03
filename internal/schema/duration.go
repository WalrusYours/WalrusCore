package schema

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a schema duration (3d, 12h, 2w, 1y); time.ParseDuration has no days, weeks
// or years. A year is 365 days.
type Duration time.Duration

var durationRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)([smhdwy])$`)

var durationUnit = map[string]time.Duration{
	"s": time.Second,
	"m": time.Minute,
	"h": time.Hour,
	"d": 24 * time.Hour,
	"w": 7 * 24 * time.Hour,
	"y": 365 * 24 * time.Hour,
}

func ParseDuration(s string) (Duration, error) {
	if s == "" {
		return 0, nil
	}
	m := durationRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid duration %q (use a number and one of s, m, h, d, w, y, e.g. 3d)", s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return Duration(n * float64(durationUnit[m[2]])), nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d Duration) String() string {
	for _, u := range []string{"w", "d", "h", "m", "s"} {
		unit := durationUnit[u]
		if d != 0 && time.Duration(d)%unit == 0 {
			return strconv.FormatInt(int64(time.Duration(d)/unit), 10) + u
		}
	}
	return "0s"
}

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }
