package rank

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/timurcravtov/walrus/internal/recommend"
)

// The instant re-rank: a knob change only re-scores. Candidates, similarities and co-listing
// counts come from the snapshot the first request built.
func TestMovingAKnobReusesTheSnapshot(t *testing.T) {
	f := newFixture(t)
	f.ask(recommend.Request{Knobs: map[string]float64{"vibe_vs_branch_out": 0}})
	f.ask(recommend.Request{Knobs: map[string]float64{"vibe_vs_branch_out": 1, "match_energy": 1}})
	f.ask(recommend.Request{Knobs: map[string]float64{"whole_vs_any": 1}, Limit: 5, Locale: "ro"})
	if n := f.ranks.builds.Load(); n != 1 {
		t.Fatalf("%d snapshots built for three requests that differ only in weights, limit and locale; want 1", n)
	}

	// a different seed, a different context or new data each need their own
	f.ask(recommend.Request{Items: []string{"acdc_bib"}})
	f.ask(recommend.Request{Context: map[string]any{"genre": "metal", "strict_genre": true}})
	if n := f.ranks.builds.Load(); n != 3 {
		t.Errorf("builds = %d after a new seed and a new context, want 3", n)
	}
	f.addTo("late", "late_mix", "acdc_bib", "zep_wll")
	f.ask(recommend.Request{Knobs: map[string]float64{"vibe_vs_branch_out": 0}})
	if n := f.ranks.builds.Load(); n != 4 {
		t.Errorf("builds = %d after new interactions, want 4: a write must never be ranked from old data", n)
	}
}

func TestCandidatesNeverDependOnTermWeightKnobs(t *testing.T) {
	f := newFixture(t)
	off := map[string]float64{"match_energy": 0, "match_mood": 0, "match_danceability": 0, "match_acousticness": 0, "match_tempo": 0}
	on := map[string]float64{"match_energy": 1, "match_mood": 1, "match_danceability": 1, "match_acousticness": 1, "match_tempo": 1}
	a := f.ask(recommend.Request{Items: []string{"acdc_bib"}, Knobs: off, Limit: 50})
	b := f.ask(recommend.Request{Items: []string{"acdc_bib"}, Knobs: on, Limit: 50})
	if a.CandidatesConsidered != b.CandidatesConsidered || len(a.Items) != len(b.Items) {
		t.Errorf("candidates %d vs %d: sliders must only re-score", a.CandidatesConsidered, b.CandidatesConsidered)
	}
}

func TestUnsupportedFeaturesAreListed(t *testing.T) {
	f := newFixture(t)
	issues := f.ranks.Unsupported(f.sch.Compiled().Schema)
	paths := map[string]string{}
	for _, is := range issues {
		paths[is.Path] = is.Message
		if strings.Contains(is.Path, "playlist_add") {
			t.Errorf("playlist_add uses only built features, got %s: %s", is.Path, is.Message)
		}
	}
	for _, want := range []string{"signals.trending.type", "signals.familiarity.type", "signals.artist_spread.type"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("no warning at %s; got %v", want, paths)
		}
	}
	for _, built := range []string{"signals.co_listed.type", "signals.sounds_like.type", "signals.collaborative.type"} {
		if msg, ok := paths[built]; ok {
			t.Errorf("%s is built but was reported: %s", built, msg)
		}
	}
}

func TestProfileSizeCountsDistinctLikedItems(t *testing.T) {
	f := newFixture(t)
	c := f.sch.Compiled()
	f.addTo("fan", "fan_a", "acdc_bib", "zep_wll")
	f.addTo("fan", "fan_b", "acdc_bib") // the same song in a second playlist
	if n, err := f.ranks.ProfileSize(context.Background(), c, "fan"); err != nil || n != 2 {
		t.Errorf("profile size = %d, %v; want 2", n, err)
	}
	if n, _ := f.ranks.ProfileSize(context.Background(), c, "nobody"); n != 0 {
		t.Errorf("a user with no history has profile size %d", n)
	}
}

func TestSnapshotKeyIgnoresWhatOnlyChangesScoring(t *testing.T) {
	base := recommend.RankInput{Recommender: "r", User: "u", Seed: recommend.Seed{Kind: "items"}}
	k := snapshotKey(base, 7)
	scoring := base
	scoring.Weights = map[string]float64{"a": 1}
	scoring.Meta = map[string]float64{"x": 1}
	scoring.Limit, scoring.Locale = 5, "ro"
	if snapshotKey(scoring, 7) != k {
		t.Error("weights, meta, limit and locale must not change the key")
	}
	if snapshotKey(base, 8) == k {
		t.Error("a new store version must change the key")
	}
	other := base
	other.Seed.Items = nil
	other.User = "v"
	if snapshotKey(other, 7) == k {
		t.Error("another user must change the key")
	}
}

// Many requests share one snapshot at once; it is only read after it is built.
func TestConcurrentRequestsShareASnapshotSafely(t *testing.T) {
	f := newFixture(t)
	f.ask(recommend.Request{}) // build it
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			knob := float64(i%5) / 4
			res, err := f.svc.Recommend(context.Background(), recommend.Request{
				Recommender: "playlist_add", Items: []string{"acdc_bib", "acdc_thunder"}, Explain: true,
				Knobs: map[string]float64{"vibe_vs_branch_out": knob, "whole_vs_any": knob, "mix_in_my_taste": 1},
			})
			if err != nil || len(res.Items) == 0 {
				t.Errorf("%v %v", err, res)
			}
		}()
	}
	wg.Wait()
	if n := f.ranks.builds.Load(); n != 1 {
		t.Errorf("builds = %d, want 1", n)
	}
}
