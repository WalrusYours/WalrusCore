package recommend

import (
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
)

// stored is one served list, kept so that explain is free.
type stored struct {
	RecID       string
	Recommender string
	Used        string
	User        domain.UserID
	Items       []domain.ScoredItem
	Reasons     map[domain.EntityID]string
	Because     map[domain.EntityID]map[string]string
	Weights     map[string]float64
	Meta        map[string]float64
	Experiment  *Assignment
	Holdout     bool
	At          time.Time
}

// resultCache holds lists by rec_id for a short time, bounded in size.
type resultCache struct {
	mu    sync.Mutex
	m     map[string]*stored
	order []string
	max   int
	ttl   time.Duration
	now   func() time.Time
}

func newResultCache(max int, ttl time.Duration) *resultCache {
	return &resultCache{m: map[string]*stored{}, max: max, ttl: ttl, now: time.Now}
}

func (c *resultCache) put(s *stored) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[s.RecID] = s
	c.order = append(c.order, s.RecID)
	for len(c.order) > c.max { // oldest first
		delete(c.m, c.order[0])
		c.order = c.order[1:]
	}
}

func (c *resultCache) get(id string) (*stored, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.m[id]
	if !ok {
		return nil, false
	}
	if c.now().Sub(s.At) > c.ttl {
		delete(c.m, id)
		return nil, false
	}
	return s, true
}

type SignalShare struct {
	Signal  string  `json:"signal"`
	Value   float64 `json:"value"`
	Because string  `json:"because,omitempty"`
}

type Breakdown struct {
	RecID       string             `json:"rec_id"`
	Item        string             `json:"item"`
	Recommender string             `json:"recommender"`
	Used        string             `json:"used"`
	Score       float64            `json:"score"`
	Position    int                `json:"position"`
	Reason      string             `json:"reason,omitempty"`
	Breakdown   []SignalShare      `json:"breakdown"`
	WeightsUsed map[string]float64 `json:"weights_used"`
	Experiment  *Assignment        `json:"experiment,omitempty"`
	Holdout     bool               `json:"holdout,omitempty"`
}

// ExplainRec explains one item of one exact list, from the stored result: the contributions are
// the ones that produced the score, so the breakdown cannot disagree with the ranking.
func (s *Service) ExplainRec(recID string, item string) (*Breakdown, error) {
	st, ok := s.results.get(recID)
	if !ok {
		return nil, fail(http.StatusNotFound, "unknown_recommendation", "%q is not a recent recommendation (lists are kept for a minute)", recID)
	}
	for i, it := range st.Items {
		if string(it.Item) != item {
			continue
		}
		parts := make([]SignalShare, 0, len(it.Signals))
		for sig, v := range it.Signals {
			parts = append(parts, SignalShare{Signal: sig, Value: v, Because: st.Because[it.Item][sig]})
		}
		sort.Slice(parts, func(a, b int) bool {
			if parts[a].Value != parts[b].Value {
				return parts[a].Value > parts[b].Value
			}
			return parts[a].Signal < parts[b].Signal
		})
		return &Breakdown{
			RecID: recID, Item: item, Recommender: st.Recommender, Used: st.Used, Score: it.Score,
			Position: i + 1, Reason: st.Reasons[it.Item], Breakdown: parts, WeightsUsed: st.Weights,
			Experiment: st.Experiment, Holdout: st.Holdout,
		}, nil
	}
	return nil, fail(http.StatusNotFound, "unknown_item", "%q is not in that list", item)
}
