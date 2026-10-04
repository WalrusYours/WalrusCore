package rank

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/ingest"
	"github.com/timurcravtov/walrus/internal/recommend"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store/memory"
)

type song struct {
	id, title, artist string
	genres            []any
	energy, valence   float64
}

func g(names ...string) []any {
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out
}

var songs = []song{
	{"acdc_bib", "Back in Black", "acdc", g("rock", "hard rock"), 0.92, 0.65},
	{"acdc_thunder", "Thunderstruck", "acdc", g("rock", "hard rock"), 0.95, 0.45},
	{"acdc_highway", "Highway to Hell", "acdc", g("rock", "hard rock"), 0.9, 0.7},
	{"metallica_sandman", "Enter Sandman", "metallica", g("metal", "hard rock"), 0.9, 0.3},
	{"sabbath_paranoid", "Paranoid", "sabbath", g("rock", "metal"), 0.9, 0.35},
	{"ozzy_crazy", "Crazy Train", "ozzy", g("rock", "metal", "hard rock"), 0.85, 0.6},
	{"zep_wll", "Whole Lotta Love", "zeppelin", g("rock", "hard rock"), 0.85, 0.45},
	{"nirvana_spirit", "Smells Like Teen Spirit", "nirvana", g("rock", "grunge"), 0.91, 0.35},
	{"queen_bohemian", "Bohemian Rhapsody", "queen", g("rock", "classic rock"), 0.6, 0.4},
	{"bon_holocene", "Holocene", "bon_iver", g("folk", "indie folk"), 0.25, 0.3},
	{"tracy_fast", "Fast Car", "tracy", g("folk", "acoustic"), 0.35, 0.35},
	{"satie_gymno", "Gymnopedie No. 1", "satie", g("classical"), 0.05, 0.3},
	{"dua_levitating", "Levitating", "dua", g("pop", "dance"), 0.83, 0.92},
}

// Other people's playlists: what the platform reports as add_to_playlist events.
var playlists = [][]string{
	{"acdc_bib", "acdc_thunder", "metallica_sandman", "sabbath_paranoid"},
	{"acdc_bib", "acdc_thunder", "zep_wll", "ozzy_crazy"},
	{"acdc_thunder", "metallica_sandman", "ozzy_crazy", "sabbath_paranoid"},
	{"acdc_bib", "zep_wll", "queen_bohemian"},
	{"bon_holocene", "tracy_fast", "satie_gymno"},
	{"acdc_bib", "acdc_thunder", "ozzy_crazy", "sabbath_paranoid"},
}

type fixture struct {
	sch   *schema.Service
	t     *testing.T
	svc   *recommend.Service
	ing   *ingest.Service
	ranks *Ranker
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("..", "..", "docs", "schema", "schema-examples", "spotify.yml"))
	if err != nil {
		t.Fatal(err)
	}
	sch := schema.NewService()
	if res := sch.Load(text, schema.LoadOptions{Author: "test"}); !res.OK {
		t.Fatalf("schema rejected: %+v", res.Errors)
	}
	st := memory.New()
	f := &fixture{t: t, sch: sch, ing: ingest.NewService(sch, st), ranks: New(st)}
	f.svc = recommend.NewService(sch, f.ranks).WithHistory(f.ranks)

	var raws []ingest.Raw
	for _, s := range songs {
		raws = append(raws, ingest.Raw{Entity: "track", ID: s.id, Attributes: map[string]any{
			"title": s.title, "artist_id": s.artist, "album_id": s.artist + "_album", "genres": s.genres,
			"language": "en", "explicit": false, "release_date": "1980-01-01T00:00:00Z", "duration_ms": 240000.0,
			"available_in": g("MD", "US"), "energy": s.energy, "valence": s.valence,
			"danceability": math.Round(s.valence*80) / 100, "acousticness": math.Round((1-s.energy)*100) / 100,
			"tempo": math.Round(80 + 80*s.energy),
		}})
	}
	f.entities(raws)

	var events []ingest.RawInteraction
	for p, tracks := range playlists {
		for k, id := range tracks {
			events = append(events, ingest.RawInteraction{
				User: fmt.Sprintf("u%d", p), Type: "add_to_playlist", Target: id,
				TS: time.Date(2026, 9, 1, 12, k, 0, 0, time.UTC).Format(time.RFC3339), Fields: map[string]any{"playlist_id": fmt.Sprintf("p%d", p)},
			})
		}
	}
	if res, err := f.ing.Interactions(context.Background(), events); err != nil || len(res.Rejected) > 0 {
		t.Fatalf("events: %+v %v", res, err)
	}
	return f
}

func (f *fixture) entities(raws []ingest.Raw) {
	f.t.Helper()
	res, err := f.ing.Entities(context.Background(), raws)
	if err != nil || len(res.Rejected) > 0 {
		f.t.Fatalf("entities: %+v %v", res, err)
	}
}

func (f *fixture) ask(req recommend.Request) *recommend.Response {
	f.t.Helper()
	if req.Recommender == "" {
		req.Recommender = "playlist_add"
	}
	if req.Items == nil && req.Recommender == "playlist_add" {
		req.Items = []string{"acdc_bib", "acdc_thunder"}
	}
	req.Explain = true
	res, err := f.svc.Recommend(context.Background(), req)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func ids(r *recommend.Response) []string {
	out := make([]string, len(r.Items))
	for i, it := range r.Items {
		out[i] = it.ID
	}
	return out
}

func TestSuggestsWhatFitsThePlaylist(t *testing.T) {
	f := newFixture(t)
	res := f.ask(recommend.Request{Limit: 20})
	got := ids(res)

	for _, id := range []string{"acdc_bib", "acdc_thunder"} {
		if slices.Contains(got, id) {
			t.Errorf("%s is already in the playlist", id)
		}
	}
	for _, id := range got[:3] {
		if strings.Contains(id, "bon_") || strings.Contains(id, "tracy_") || strings.Contains(id, "satie_") || strings.Contains(id, "dua_") {
			t.Errorf("a rock and metal playlist should be suggested rock and metal first: %v", got)
		}
	}
	if got[0] != "ozzy_crazy" && got[0] != "sabbath_paranoid" {
		t.Errorf("the songs people add next to these two should lead: %v", got)
	}
	if last := got[len(got)-2:]; !slices.Contains(last, "bon_holocene") && !slices.Contains(last, "dua_levitating") {
		t.Errorf("songs that sound and read nothing like the playlist should come last: %v", got)
	}
	if res.CandidatesConsidered == 0 || res.Items[0].Reason == "" {
		t.Errorf("candidates=%d reason=%q", res.CandidatesConsidered, res.Items[0].Reason)
	}
	for _, it := range res.Items {
		if it.Score <= 0 || it.Type != "track" {
			t.Errorf("%+v", it)
		}
	}
}

func TestEveryScoreIsTheSumOfItsSignalContributions(t *testing.T) {
	f := newFixture(t)
	res := f.ask(recommend.Request{})
	for _, it := range res.Items {
		b, err := f.svc.ExplainRec(res.RecID, it.ID)
		if err != nil {
			t.Fatal(err)
		}
		sum := 0.0
		for _, s := range b.Breakdown {
			sum += s.Value
		}
		if math.Abs(sum-it.Score) > 1e-9 {
			t.Errorf("%s: breakdown %v sums to %v, score is %v", it.ID, b.Breakdown, sum, it.Score)
		}
	}
}

func TestTheReasonNamesASeedSong(t *testing.T) {
	f := newFixture(t)
	res := f.ask(recommend.Request{})
	reason := res.Items[0].Reason
	if !strings.Contains(reason, "Back in Black") && !strings.Contains(reason, "Thunderstruck") {
		t.Errorf("reason = %q", reason)
	}
	if strings.Contains(reason, "{") {
		t.Errorf("an unfilled placeholder: %q", reason)
	}
}

func TestMovingAKnobReRanksTheSameCandidates(t *testing.T) {
	f := newFixture(t)
	vibe := f.ask(recommend.Request{Limit: 20, Knobs: map[string]float64{"vibe_vs_branch_out": 0}})
	branch := f.ask(recommend.Request{Limit: 20, Knobs: map[string]float64{"vibe_vs_branch_out": 1}})

	if vibe.CandidatesConsidered != branch.CandidatesConsidered {
		t.Errorf("candidates differ: %d vs %d; they must not depend on weights", vibe.CandidatesConsidered, branch.CandidatesConsidered)
	}
	if slices.Equal(ids(vibe), ids(branch)) {
		t.Errorf("the knob should change the order: %v", ids(vibe))
	}
	pos := func(r *recommend.Response, id string) int { return slices.Index(ids(r), id) }
	if !(pos(branch, "dua_levitating") < pos(vibe, "dua_levitating")) {
		t.Errorf("branching out should lift a song nobody has added anywhere: vibe %d, branch %d", pos(vibe, "dua_levitating"), pos(branch, "dua_levitating"))
	}
}

func TestAHeavierPopularityWeightFavoursWhatPeopleAdd(t *testing.T) {
	f := newFixture(t)
	deep := f.ask(recommend.Request{Limit: 20, Knobs: map[string]float64{"deep_cuts_vs_hits": 0}})
	hits := f.ask(recommend.Request{Limit: 20, Knobs: map[string]float64{"deep_cuts_vs_hits": 1}})
	if deep.Weights["popularity"] >= hits.Weights["popularity"] {
		t.Fatalf("weights: %v vs %v", deep.Weights["popularity"], hits.Weights["popularity"])
	}
	score := func(r *recommend.Response, id string) float64 {
		for _, it := range r.Items {
			if it.ID == id {
				return it.Score
			}
		}
		return -1
	}
	if !(score(hits, "metallica_sandman") > score(deep, "metallica_sandman")) {
		t.Errorf("a song in three playlists should gain from the hits knob")
	}
}

func TestStrictGenreIsAHardFilter(t *testing.T) {
	f := newFixture(t)
	loose := f.ask(recommend.Request{Limit: 20, Context: map[string]any{"genre": "metal"}})
	if !slices.Contains(ids(loose), "zep_wll") {
		t.Errorf("without strict_genre nothing is filtered: %v", ids(loose))
	}
	strict := f.ask(recommend.Request{Limit: 20, Context: map[string]any{"genre": "metal", "strict_genre": true}})
	want := []string{"metallica_sandman", "ozzy_crazy", "sabbath_paranoid"}
	got := ids(strict)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("strict metal = %v, want %v", got, want)
	}
}

func TestOneSongPerArtistInEveryTen(t *testing.T) {
	f := newFixture(t)
	// the playlist holds one AC/DC song; a second one would rank high without the rule
	got := ids(f.ask(recommend.Request{Limit: 20, Items: []string{"acdc_bib"}}))
	i, j := slices.Index(got, "acdc_thunder"), slices.Index(got, "acdc_highway")
	if i < 0 || j < 0 {
		t.Fatalf("both other AC/DC songs should be candidates: %v", got)
	}
	if math.Abs(float64(i-j)) < 2 {
		t.Errorf("two songs by one artist sit next to each other: %v", got)
	}
}

func TestAnEmptyPlaylistFallsBackToItsTitle(t *testing.T) {
	f := newFixture(t)
	res := f.ask(recommend.Request{Items: []string{}, Limit: 5, Context: map[string]any{"title_words": []any{"folk"}}})
	if res.Used != "from_title" {
		t.Fatalf("used = %s", res.Used)
	}
	top := ids(res)[:2]
	slices.Sort(top)
	if !slices.Equal(top, []string{"bon_holocene", "tracy_fast"}) {
		t.Errorf("a playlist called folk should start with the folk songs: %v", ids(res))
	}
}

func TestWhatItCannotDoYetIsSkippedNotFatal(t *testing.T) {
	f := newFixture(t)
	// the feed uses own_history, trend and a diversity re-rank, which are not built yet
	res := f.ask(recommend.Request{Recommender: "home", User: "u0", Limit: 5})
	if len(res.Items) == 0 {
		t.Fatal("the signals that are built should still rank")
	}
	b, err := f.svc.ExplainRec(res.RecID, res.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range b.Breakdown {
		if s.Signal == "familiarity" || s.Signal == "trending" || s.Signal == "artist_spread" {
			t.Errorf("%s is not built yet; it should be absent", s.Signal)
		}
	}
}

func TestUnknownSeedSongsAreIgnored(t *testing.T) {
	f := newFixture(t)
	res := f.ask(recommend.Request{Items: []string{"no_such_song", "acdc_bib"}})
	if len(res.Items) == 0 {
		t.Error("the known seed song should still give suggestions")
	}
	if none := f.ask(recommend.Request{Items: []string{"no_such_song"}}); len(none.Items) == 0 {
		t.Log("an all-unknown seed still lists candidates, ranked on what does not need a seed")
	}
}

func TestOptionalAttributesAreNotRequired(t *testing.T) {
	f := newFixture(t)
	f.entities([]ingest.Raw{{Entity: "track", ID: "extra", Attributes: map[string]any{
		"artist_id": "x", "album_id": "x", "genres": g("rock"), "language": "en", "explicit": false,
		"release_date": "2000-01-01T00:00:00Z", "duration_ms": 1000.0, "available_in": g("MD"),
		"energy": 0.9, "valence": 0.5, "danceability": 0.5, "acousticness": 0.1, "tempo": 100.0,
	}}})
	if res := f.ask(recommend.Request{Limit: 20}); !slices.Contains(ids(res), "extra") {
		t.Errorf("a track without a title or an embedding is still a candidate: %v", ids(res))
	}
}

func TestNormaliseRescalesAndTreatsAConstantAsNoInformation(t *testing.T) {
	got := normalise([]float64{2, 4, 6}, "")
	if got[0] != 0 || got[1] != 0.5 || got[2] != 1 {
		t.Errorf("minmax = %v", got)
	}
	for _, v := range normalise([]float64{3, 3, 3}, "") {
		if v != 0.5 {
			t.Errorf("constant = %v", v)
		}
	}
	if r := normalise([]float64{10, 1000, 100}, "rank"); r[0] >= r[2] || r[2] >= r[1] {
		t.Errorf("rank = %v", r)
	}
	if n := normalise([]float64{1, 2}, "none"); n[0] != 1 || n[1] != 2 {
		t.Errorf("none = %v", n)
	}
}

func TestCombineBlendsMeanAndBest(t *testing.T) {
	x := &ranking{}
	vals := []float64{0.2, 0.4, 0.9}
	for agg, want := range map[float64]float64{0: 0.5, 1: 0.9, 0.5: 0.7} {
		x.seedAgg = agg
		if got := x.combine(vals); math.Abs(got-want) > 1e-9 {
			t.Errorf("aggregate %v = %v, want %v", agg, got, want)
		}
	}
	if x.combine(nil) != 0 {
		t.Error("nothing to combine is 0")
	}
}

func entity(i int, nums map[string]float64) domain.Entity {
	attrs := map[string]domain.Value{}
	for k, v := range nums {
		attrs[k] = domain.Num(v)
	}
	return domain.Entity{Type: "track", ID: domain.EntityID(fmt.Sprint("e", i)), Attrs: attrs}
}
