package recommend

import (
	"slices"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/scoring"
	"github.com/timurcravtov/walrus/internal/similarity"
)

func track(id string, genres ...string) domain.Entity {
	return domain.Entity{Type: "track", ID: domain.EntityID(id), Attrs: map[string]domain.Value{"genres": domain.Set(genres...)}}
}

func TestEndToEnd(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	items := []domain.Entity{
		track("a", "rock", "indie"),
		track("b", "rock", "indie"),        // sim 1.0 to a
		track("c", "rock", "pop"),          // 1/3 to a
		track("d", "jazz"),                 // unrelated
		track("e", "pop", "dance"),         // 1/3 to c, nothing shared with a
		track("f", "rock", "indie", "pop"), // 2/3 to a
	}
	rules := map[string]schema.Interaction{
		"save": {Name: "save", Weight: 4, HalfLife: 90 * 24 * time.Hour},
		"skip": {Name: "skip", Weight: -1.5, HalfLife: 14 * 24 * time.Hour},
	}
	history := []domain.Interaction{
		{User: "u", Type: "save", Target: "a", TS: now.Add(-24 * time.Hour)},
		{User: "u", Type: "skip", Target: "d", TS: now.Add(-24 * time.Hour)},
	}

	p, err := scoring.Profile(rules, history, now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p["a"] <= 0 || p["d"] >= 0 {
		t.Fatalf("save should be positive and skip negative: %v", p)
	}

	got := Recommend(Input{
		Profile:   p,
		Neighbors: similarity.ItemNeighbors(items, "genres", 10),
		Limit:     10,
	})

	var ids []domain.EntityID
	for _, r := range got {
		ids = append(ids, r.Item)
	}
	want := []domain.EntityID{"b", "f", "c"}
	if !slices.Equal(ids, want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	if got[0].Signals[Content] != got[0].Score {
		t.Fatal("signal breakdown must equal the score")
	}
	if !slices.Equal(got[0].Via, []domain.EntityID{"a"}) {
		t.Fatalf("via: %v", got[0].Via)
	}
}

func TestKnownItemsAndDislikesNeverSeed(t *testing.T) {
	nb := map[domain.EntityID][]similarity.ItemNeighbor{
		"liked":    {{Item: "x", Sim: 0.5}},
		"disliked": {{Item: "y", Sim: 0.9}, {Item: "liked", Sim: 1}},
	}
	got := Recommend(Input{Profile: map[domain.EntityID]float64{"liked": 2, "disliked": -1}, Neighbors: nb})
	if len(got) != 1 || got[0].Item != "x" {
		t.Fatalf("only x should be recommended: %+v", got)
	}
}

func TestDislikeLowersScore(t *testing.T) {
	nb := map[domain.EntityID][]similarity.ItemNeighbor{
		"liked":    {{Item: "x", Sim: 0.8}},
		"disliked": {{Item: "x", Sim: 0.8}},
	}
	plain := Recommend(Input{Profile: map[domain.EntityID]float64{"liked": 2}, Neighbors: nb})
	mixed := Recommend(Input{Profile: map[domain.EntityID]float64{"liked": 2, "disliked": -2}, Neighbors: nb})
	if mixed[0].Score >= plain[0].Score {
		t.Fatalf("dislike should lower the score: %v vs %v", mixed[0].Score, plain[0].Score)
	}
}

func TestProfileDecay(t *testing.T) {
	now := time.Now()
	rules := map[string]schema.Interaction{"save": {Name: "save", Weight: 4, HalfLife: 10 * 24 * time.Hour}}
	h := []domain.Interaction{{Type: "save", Target: "a", TS: now.Add(-10 * 24 * time.Hour)}}
	p, _ := scoring.Profile(rules, h, now, 1)
	if p["a"] < 1.99 || p["a"] > 2.01 {
		t.Fatalf("one half-life should halve the weight: %v", p["a"])
	}
	p, _ = scoring.Profile(rules, h, now, 2) // longer horizon, slower decay
	if p["a"] <= 2.5 {
		t.Fatalf("horizon scale 2 should decay slower: %v", p["a"])
	}
}
