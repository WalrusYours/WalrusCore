package schema

import (
	"sync"
	"time"
)

type Version struct {
	Version int       `json:"version"`
	Hash    string    `json:"hash"`
	Author  string    `json:"author"`
	At      time.Time `json:"at"`
	Verdict Verdict   `json:"verdict"`
	YAML    string    `json:"yaml"`
}

type LoadOptions struct {
	DryRun          bool
	ConfirmBreaking bool
	Author          string
}

type Result struct {
	OK           bool       `json:"ok"`
	Errors       []Issue    `json:"errors"`
	Diff         DiffResult `json:"diff"`
	Version      int        `json:"version,omitempty"`
	NeedsConfirm bool       `json:"needsConfirm,omitempty"`
	Message      string     `json:"message"`
}

// Service holds the schema versions. In-memory for now; the store interface replaces the
// slice when persistence arrives.
type Service struct {
	mu       sync.RWMutex
	versions []Version
	now      func() time.Time
}

func NewService() *Service { return &Service{now: time.Now} }

func (s *Service) Current() (Version, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.versions) == 0 {
		return Version{}, false
	}
	return s.versions[len(s.versions)-1], true
}

// History returns the versions, newest first.
func (s *Service) History() []Version {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Version, len(s.versions))
	for i, v := range s.versions {
		out[len(s.versions)-1-i] = v
	}
	return out
}

// Load validates a schema and, unless DryRun, activates it as the next version.
// A breaking change needs ConfirmBreaking. An unchanged schema creates no new version.
func (s *Service) Load(yamlText []byte, o LoadOptions) Result {
	parsed, err := Parse(yamlText)
	if err != nil {
		return Result{Errors: []Issue{{Message: err.Error()}}, Diff: DiffResult{Verdict: VerdictNone, Changes: []string{}}, Message: "could not parse the schema"}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var current *Schema
	var cur Version
	if n := len(s.versions); n > 0 {
		cur = s.versions[n-1]
		current, _ = Parse([]byte(cur.YAML))
	}
	diff := Diff(current, parsed)

	if issues := Validate(parsed); len(issues) > 0 {
		return Result{Errors: issues, Diff: diff, Message: plural(len(issues), "validation error")}
	}
	if o.DryRun {
		return Result{OK: true, Errors: []Issue{}, Diff: diff, Message: "dry run: nothing was changed"}
	}
	if current != nil && diff.Verdict == VerdictNone {
		return Result{OK: true, Errors: []Issue{}, Diff: diff, Version: cur.Version, Message: "unchanged: no new version"}
	}
	if current != nil && diff.Verdict == VerdictBreaking && !o.ConfirmBreaking {
		return Result{
			Errors: []Issue{}, Diff: diff, NeedsConfirm: true,
			Message: "breaking change: stored data must be re-imported and similarity recomputed. Confirm to apply.",
		}
	}

	v := Version{
		Version: cur.Version + 1,
		Hash:    Hash(parsed),
		Author:  o.Author,
		At:      s.now().UTC(),
		Verdict: diff.Verdict,
		YAML:    string(yamlText),
	}
	s.versions = append(s.versions, v)
	return Result{OK: true, Errors: []Issue{}, Diff: diff, Version: v.Version, Message: "applied as version " + itoa(v.Version)}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return itoa(n) + " " + noun + "s"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
