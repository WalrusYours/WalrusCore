package rank

import (
	"slices"
	"testing"

	"github.com/timurcravtov/walrus/internal/recommend"
)

// What a song sounds like is several numbers, and each has its own slider. Each slider sets the
// weight of one similarity term, so moving one changes how songs are compared, not how much
// the whole "sounds like" counts.
func TestEachSoundSliderSteersWhatSoundsAlike(t *testing.T) {
	f := newFixture(t)
	only := func(slider string) map[string]float64 {
		k := map[string]float64{"match_energy": 0, "match_mood": 0, "match_danceability": 0, "match_acousticness": 0, "match_tempo": 0}
		k[slider] = 1
		return k
	}
	soundsLike := func(res *recommend.Response, id string) float64 {
		for _, s := range f.breakdown(res, id).Breakdown {
			if s.Signal == "sounds_like" {
				return s.Value
			}
		}
		t.Fatalf("%s has no sounds_like", id)
		return 0
	}

	// the seed has energy 0.92 and valence 0.65: Teen Spirit is level with it on energy (0.91) and far
	// on mood (0.35); Crazy Train is the other way round (0.85 and 0.60)
	energy := f.ask(recommend.Request{Items: []string{"acdc_bib"}, Limit: 30, Knobs: only("match_energy")})
	if !(soundsLike(energy, "nirvana_spirit") > soundsLike(energy, "ozzy_crazy")) {
		t.Errorf("matching on energy: nirvana %v, ozzy %v", soundsLike(energy, "nirvana_spirit"), soundsLike(energy, "ozzy_crazy"))
	}
	mood := f.ask(recommend.Request{Items: []string{"acdc_bib"}, Limit: 30, Knobs: only("match_mood")})
	if !(soundsLike(mood, "ozzy_crazy") > soundsLike(mood, "nirvana_spirit")) {
		t.Errorf("matching on mood: ozzy %v, nirvana %v", soundsLike(mood, "ozzy_crazy"), soundsLike(mood, "nirvana_spirit"))
	}

	// and the reason names the number that was matched
	b := f.breakdown(energy, "nirvana_spirit")
	for _, s := range b.Breakdown {
		if s.Signal == "sounds_like" && s.Because != "Close to Back in Black in energy" {
			t.Errorf("because = %q", s.Because)
		}
	}
}

func TestASliderWithNothingToCompareLeavesNoSoundSignal(t *testing.T) {
	f := newFixture(t)
	off := map[string]float64{"match_energy": 0, "match_mood": 0, "match_danceability": 0, "match_acousticness": 0, "match_tempo": 0}
	res := f.ask(recommend.Request{Items: []string{"acdc_bib"}, Limit: 30, Knobs: off})
	var values []float64
	for _, it := range res.Items {
		for _, s := range f.breakdown(res, it.ID).Breakdown {
			if s.Signal == "sounds_like" {
				values = append(values, s.Value)
			}
		}
	}
	if len(values) == 0 || slices.Max(values) != slices.Min(values) {
		t.Errorf("with every sound term at 0 the signal is the same for all songs, got %v", values)
	}
}

func TestOnlyAKnobTheRecommenderOffersSetsATermWeight(t *testing.T) {
	f := newFixture(t)
	sch := f.sch.Compiled().Schema
	terms := []term{{id: "energy", weight: 0.08}, {id: "genres", weight: 0.25}}
	meta := map[string]float64{"similarity.track.energy.weight": 0.2}
	for_ := func(recommender string) *ranking {
		return &ranking{
			snapshot: &snapshot{sch: sch, typ: "track", recommender: recommender, spec: sch.Recommenders[recommender], terms: terms},
			in:       recommend.RankInput{Meta: meta},
		}
	}

	if got := for_("home").termWeights(); !slices.Equal(got, []float64{0.08, 0.25}) {
		t.Errorf("the feed offers no energy slider, so the schema's weights stay: %v", got)
	}
	if got := for_("playlist_add").termWeights(); !slices.Equal(got, []float64{0.2, 0.25}) {
		t.Errorf("playlist_add offers match_energy, so the knob's value is that term's weight: %v", got)
	}
	meta["similarity.track.energy.weight"] = -1
	if got := for_("playlist_add").termWeights(); got[0] != 0 {
		t.Errorf("a weight cannot go below zero: %v", got)
	}
}
