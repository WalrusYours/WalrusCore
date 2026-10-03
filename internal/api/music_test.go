package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/recommend"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/similarity"
)

// A small music catalogue. Energy and valence are 0..1 audio features; plays is the popularity.
type song struct {
	id, title, artist string
	genres            []string
	energy, valence   float64
	plays             float64
}

var catalogue = []song{
	{"back_in_black", "Back in Black", "AC/DC", []string{"rock", "hard rock"}, 0.92, 0.65, 1500},
	{"thunderstruck", "Thunderstruck", "AC/DC", []string{"rock", "hard rock"}, 0.95, 0.45, 1300},
	{"enter_sandman", "Enter Sandman", "Metallica", []string{"metal", "hard rock"}, 0.90, 0.30, 1500},
	{"paranoid", "Paranoid", "Black Sabbath", []string{"rock", "metal"}, 0.90, 0.35, 700},
	{"crazy_train", "Crazy Train", "Ozzy Osbourne", []string{"rock", "metal", "hard rock"}, 0.85, 0.60, 600},
	{"whole_lotta_love", "Whole Lotta Love", "Led Zeppelin", []string{"rock", "hard rock"}, 0.85, 0.45, 900},
	{"teen_spirit", "Smells Like Teen Spirit", "Nirvana", []string{"rock", "grunge", "alternative"}, 0.91, 0.35, 1600},
	{"mr_brightside", "Mr. Brightside", "The Killers", []string{"rock", "indie"}, 0.90, 0.50, 1900},
	{"bohemian", "Bohemian Rhapsody", "Queen", []string{"rock", "classic rock"}, 0.60, 0.40, 2100},
	{"hotel_california", "Hotel California", "Eagles", []string{"rock", "classic rock", "folk rock"}, 0.45, 0.35, 1500},
	{"wonderwall", "Wonderwall", "Oasis", []string{"rock", "britpop"}, 0.55, 0.45, 1700},
	{"blinding_lights", "Blinding Lights", "The Weeknd", []string{"pop", "synth-pop"}, 0.80, 0.33, 4000},
	{"levitating", "Levitating", "Dua Lipa", []string{"pop", "dance"}, 0.83, 0.92, 2500},
	{"shake_it_off", "Shake It Off", "Taylor Swift", []string{"pop"}, 0.80, 0.94, 2200},
	{"strobe", "Strobe", "deadmau5", []string{"electronic", "progressive house"}, 0.55, 0.30, 300},
	{"clair_de_lune", "Clair de Lune", "Debussy", []string{"classical"}, 0.10, 0.30, 400},
}

// Other people's playlists: which songs end up together (the co_listed signal).
var playlists = [][]string{
	{"back_in_black", "thunderstruck", "enter_sandman", "paranoid"},
	{"back_in_black", "thunderstruck", "whole_lotta_love", "crazy_train"},
	{"thunderstruck", "enter_sandman", "crazy_train", "paranoid"},
	{"back_in_black", "whole_lotta_love", "bohemian", "hotel_california"},
	{"teen_spirit", "mr_brightside", "wonderwall"},
	{"blinding_lights", "levitating", "shake_it_off"},
	{"strobe", "blinding_lights"},
}

// catalogueRanker scores every song not already in the playlist on the signals of playlist_add,
// from the catalogue above, then weights them with the numbers the engine resolved. (The real
// scorer will read these from the store; the arithmetic here is a stand-in for it.)
type catalogueRanker struct{ got recommend.RankInput }

func (c *catalogueRanker) Rank(_ context.Context, in recommend.RankInput) (recommend.RankOutput, error) {
	c.got = in
	byID := map[string]song{}
	maxPlays := 0.0
	for _, s := range catalogue {
		byID[s.id] = s
		maxPlays = math.Max(maxPlays, s.plays)
	}
	inSeed := map[string]bool{}
	var seed []song
	for _, id := range in.Seed.Items {
		inSeed[string(id)] = true
		seed = append(seed, byID[string(id)])
	}
	meanEnergy := 0.0
	for _, s := range seed {
		meanEnergy += s.energy / float64(len(seed))
	}

	var out recommend.RankOutput
	for _, cand := range catalogue {
		if inSeed[cand.id] {
			continue // constraint: not_in_seed
		}
		var sounds, genres, coListed float64
		for _, s := range seed {
			sounds += (1 - math.Hypot(cand.energy-s.energy, cand.valence-s.valence)/math.Sqrt2) / float64(len(seed))
			genres += similarity.Jaccard(domain.Set(cand.genres...), domain.Set(s.genres...)) / float64(len(seed))
			with, both := 0, 0
			for _, p := range playlists {
				if slices.Contains(p, s.id) {
					with++
					if slices.Contains(p, cand.id) {
						both++
					}
				}
			}
			if with > 0 {
				coListed += float64(both) / float64(with) / float64(len(seed))
			}
		}
		raw := map[string]float64{
			"co_listed":   coListed,
			"sounds_like": sounds,
			"genre_fit":   genres,
			"energy_fit":  1 - math.Abs(cand.energy-meanEnergy),
			"popularity":  cand.plays / maxPlays,
			"exploration": 1 - cand.plays/maxPlays,
		}
		it := domain.ScoredItem{Item: domain.EntityID(cand.id), Signals: map[string]float64{}}
		for sig, v := range raw {
			it.Signals[sig] = v * in.Weights[sig]
			it.Score += it.Signals[sig]
		}
		out.Items = append(out.Items, it)
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Score > out.Items[j].Score })
	out.Candidates = len(out.Items)
	return out, nil
}

// "You might also add" on the music schema: an AC/DC-and-metal playlist, asked with the default
// Tune, then with the sliders moved. The tables in the log (go test -v) are the point.
func TestMusicRecommendationOnTheMusicSchema(t *testing.T) {
	ranker := &catalogueRanker{}
	srv := httptest.NewServer(New(Config{
		AdminKey: adminKey, InstanceID: "abc123", InstanceName: "test", Version: "t",
		FailDelay: -1, MaxSchemaBytes: 64 << 10,
	}, schema.NewService(), WithRanker(ranker)))
	t.Cleanup(srv.Close)
	pushExample(t, srv, "spotify.yml")

	byID := map[string]song{}
	for _, s := range catalogue {
		byID[s.id] = s
	}
	line := func(s song) string {
		return fmt.Sprintf("%-24s %-14s %-24s energy %.2f  valence %.2f  plays %4.0f",
			s.title, s.artist, strings.Join(s.genres, ", "), s.energy, s.valence, s.plays)
	}

	seed := []string{"back_in_black", "thunderstruck", "enter_sandman"}
	t.Log("PLAYLIST \"Garage rock night\":")
	for _, id := range seed {
		t.Log("   " + line(byID[id]))
	}
	quoted := `"` + strings.Join(seed, `","`) + `"`

	ask := func(label, extra string) recommend.Response {
		t.Helper()
		body := `{"items":[` + quoted + `],"limit":5` + extra + `}`
		res, raw := do(t, srv, "POST", "/v1/recommenders/playlist_add/recommend", body, bearer)
		if res.StatusCode != 200 {
			t.Fatalf("%d %s", res.StatusCode, raw)
		}
		var r recommend.Response
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		t.Logf("\n%s\n   request: %s", label, body)
		t.Logf("   weights: co_listed %.2f  sounds_like %.2f  genre_fit %.2f  energy_fit %.2f  popularity %.2f  exploration %.2f",
			r.Weights["co_listed"], r.Weights["sounds_like"], r.Weights["genre_fit"],
			r.Weights["energy_fit"], r.Weights["popularity"], r.Weights["exploration"])
		for i, it := range r.Items {
			t.Logf("   %d. score %.3f  %s", i+1, it.Score, line(byID[it.ID]))
		}
		return r
	}

	base := ask("DEFAULT TUNE", "")
	branch := ask(`TUNE: vibe_vs_branch_out = 1 ("branch out")`, `,"knobs":{"vibe_vs_branch_out":1}`)
	hits := ask(`TUNE: deep_cuts_vs_hits = 1 ("hits")`, `,"knobs":{"deep_cuts_vs_hits":1}`)

	// a rock/metal list gets rock and metal first, and never what is already in it
	for _, it := range base.Items[:3] {
		g := strings.Join(byID[it.ID].genres, " ")
		if !strings.Contains(g, "rock") && !strings.Contains(g, "metal") {
			t.Errorf("the default top 3 should stay in rock and metal: %+v", base.Items)
		}
	}
	for _, r := range []recommend.Response{base, branch, hits} {
		for _, it := range r.Items {
			if slices.Contains(seed, it.ID) {
				t.Errorf("%s is already in the playlist", it.ID)
			}
		}
	}

	// the knobs move the list: branch out favours songs few people play, hits the popular ones
	avg := func(r recommend.Response) float64 {
		sum := 0.0
		for _, it := range r.Items {
			sum += byID[it.ID].plays
		}
		return sum / float64(len(r.Items))
	}
	t.Logf("\naverage plays of the 5 suggestions: branch out %.0f, default %.0f, hits %.0f", avg(branch), avg(base), avg(hits))
	if !(avg(branch) < avg(base) && avg(base) < avg(hits)) {
		t.Errorf("branch out < default < hits expected, got %.0f %.0f %.0f", avg(branch), avg(base), avg(hits))
	}

	// explain: the breakdown of the top suggestion is the signals that scored it
	top := base.Items[0].ID
	res, raw := do(t, srv, "GET", "/v1/recommendations/"+base.RecID+"/explain/"+top, "", bearer)
	var b recommend.Breakdown
	_ = json.Unmarshal(raw, &b)
	if res.StatusCode != 200 || len(b.Breakdown) == 0 {
		t.Fatalf("explain = %d %s", res.StatusCode, raw)
	}
	t.Logf("\nWHY %q (rec_id %s):", byID[top].title, base.RecID)
	sum := 0.0
	for _, s := range b.Breakdown {
		t.Logf("   %-12s %.3f", s.Signal, s.Value)
		sum += s.Value
	}
	if math.Abs(sum-b.Score) > 1e-9 {
		t.Errorf("the breakdown (%.6f) must add up to the score (%.6f)", sum, b.Score)
	}

	// a feed knob is not offered by this recommender
	res, raw = do(t, srv, "POST", "/v1/recommenders/playlist_add/recommend",
		`{"items":[`+quoted+`],"knobs":{"taste_vs_crowd":1}}`, bearer)
	e := wantError(t, res, raw, 400, "validation_error")
	t.Logf("\ntaste_vs_crowd on playlist_add -> %d %s", res.StatusCode, e.Error.Message)
}
