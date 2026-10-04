package rank

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/ingest"
	"github.com/timurcravtov/walrus/internal/recommend"
)

// addTo makes a user put songs in one playlist, the way the platform reports it.
func (f *fixture) addTo(user, playlist string, songs ...string) {
	f.t.Helper()
	var events []ingest.RawInteraction
	for k, id := range songs {
		events = append(events, ingest.RawInteraction{
			User: user, Type: "add_to_playlist", Target: id,
			TS: time.Date(2026, 9, 20, 12, k, 0, 0, time.UTC).Format(time.RFC3339), Fields: map[string]any{"playlist_id": playlist},
		})
	}
	if res, err := f.ing.Interactions(context.Background(), events); err != nil || len(res.Rejected) > 0 {
		f.t.Fatalf("events: %+v %v", res, err)
	}
}

func (f *fixture) breakdown(res *recommend.Response, id string) *recommend.Breakdown {
	f.t.Helper()
	b, err := f.svc.ExplainRec(res.RecID, id)
	if err != nil {
		f.t.Fatalf("%s: %v (list: %v)", id, err, ids(res))
	}
	return b
}

func TestPeopleWithSimilarTasteLeadToSuggestions(t *testing.T) {
	f := newFixture(t)
	// u4 collected the quiet songs; someone building a playlist with one of them is like u4
	res := f.ask(recommend.Request{Items: []string{"bon_holocene"}, Limit: 20})

	for _, id := range []string{"tracy_fast", "satie_gymno"} {
		if !slices.Contains(ids(res), id) {
			t.Fatalf("%s should be suggested: %v", id, ids(res))
		}
		var collab float64
		var because string
		for _, s := range f.breakdown(res, id).Breakdown {
			if s.Signal == "collaborative" {
				collab, because = s.Value, s.Because
			}
		}
		if collab <= 0 || !strings.Contains(because, "similar taste") {
			t.Errorf("%s: collaborative = %v %q; it is in the playlist of someone with the same taste", id, collab, because)
		}
	}
	// nobody who likes bon_holocene has a rock song, so the people signal says nothing about one
	for _, s := range f.breakdown(res, "acdc_bib").Breakdown {
		if s.Signal == "collaborative" && s.Value > 0 {
			t.Errorf("acdc_bib got %v from people who share nothing with this playlist", s.Value)
		}
	}
}

// Item similarity and user similarity are different evidence. Two people who both have a rock song
// and a classical piece say nothing about how those songs sound or about how often playlists
// hold them (co_listed needs three playlists), yet they are a reason to suggest the one to
// someone who has the other.
func TestUserSimilarityFindsWhatItemSimilarityCannot(t *testing.T) {
	f := newFixture(t)
	f.addTo("w1", "w1_mix", "acdc_bib", "satie_gymno")
	f.addTo("w2", "w2_mix", "acdc_bib", "satie_gymno")

	res := f.ask(recommend.Request{Items: []string{"acdc_bib"}, Limit: 20})
	b := f.breakdown(res, "satie_gymno")
	got := map[string]float64{}
	for _, s := range b.Breakdown {
		got[s.Signal] = s.Value
	}
	if got["collaborative"] <= 0 {
		t.Errorf("collaborative = %v: two people with the same two songs", got["collaborative"])
	}
	if got["co_listed"] != 0 {
		t.Errorf("co_listed = %v: only two playlists hold them together, the schema needs three", got["co_listed"])
	}
	if got["genre_fit"] != 0 {
		t.Errorf("genre_fit = %v: a rock and a classical song share no genre", got["genre_fit"])
	}
}

func TestOnlyThePeopleMostLikeTheQueryCount(t *testing.T) {
	f := newFixture(t)
	// a close match to the seed (two shared songs) and a distant one (one shared song among many)
	f.addTo("close", "p_close", "acdc_bib", "acdc_thunder", "queen_bohemian")
	f.addTo("far", "p_far", "acdc_bib", "tracy_fast", "bon_holocene", "satie_gymno", "dua_levitating")

	res := f.ask(recommend.Request{Items: []string{"acdc_bib", "acdc_thunder"}, Limit: 30})
	value := func(id string) float64 {
		for _, s := range f.breakdown(res, id).Breakdown {
			if s.Signal == "collaborative" {
				return s.Value
			}
		}
		return 0
	}
	if !(value("queen_bohemian") > value("dua_levitating")) {
		t.Errorf("a close match's song should count for more than a distant one's: %v vs %v", value("queen_bohemian"), value("dua_levitating"))
	}
}

func TestTheRequestersOwnHistoryIsNotTheirNeighbour(t *testing.T) {
	f := newFixture(t)
	// "me" has the only copy of these songs; asking as "me" must not recommend them back from "me"
	f.addTo("me", "p_me", "acdc_bib", "wonderwall_only")
	f.entities([]ingest.Raw{{Entity: "track", ID: "wonderwall_only", Attributes: map[string]any{
		"artist_id": "oasis", "album_id": "oasis_album", "genres": g("rock", "britpop"), "language": "en", "explicit": false,
		"release_date": "1995-01-01T00:00:00Z", "duration_ms": 258000.0, "available_in": g("MD"),
		"energy": 0.55, "valence": 0.45, "danceability": 0.4, "acousticness": 0.4, "tempo": 118.0,
	}}})

	own := f.ask(recommend.Request{Items: []string{"acdc_bib"}, User: "me", Limit: 40})
	other := f.ask(recommend.Request{Items: []string{"acdc_bib"}, User: "someone_else", Limit: 40})
	collab := func(res *recommend.Response) float64 {
		if !slices.Contains(ids(res), "wonderwall_only") {
			return 0
		}
		for _, s := range f.breakdown(res, "wonderwall_only").Breakdown {
			if s.Signal == "collaborative" {
				return s.Value
			}
		}
		return 0
	}
	if collab(own) != 0 {
		t.Errorf("a user was their own neighbour: collaborative = %v", collab(own))
	}
	if collab(other) <= 0 {
		t.Errorf("another user should get it from \"me\": %v", collab(other))
	}
}

// A play counts with its value (the share of the song listened to), as the schema's transform
// says, not as a flat 1.
func TestInteractionValuesShapeTaste(t *testing.T) {
	f := newFixture(t)
	one, tenth := 1.0, 0.1
	at := time.Now().UTC().Format(time.RFC3339)
	if res, err := f.ing.Interactions(context.Background(), []ingest.RawInteraction{
		{User: "full", Type: "play", Target: "zep_wll", Value: &one, TS: at},
		{User: "skim", Type: "play", Target: "zep_wll", Value: &tenth, TS: at},
	}); err != nil || len(res.Rejected) > 0 {
		t.Fatalf("%+v %v", res, err)
	}
	c := f.sch.Compiled()
	s, err := f.ranks.build(context.Background(), recommend.RankInput{
		Compiled: c, Recommender: "playlist_add", Spec: c.Schema.Recommenders["playlist_add"], Seed: recommend.Seed{Kind: "items"},
	})
	if err != nil {
		t.Fatal(err)
	}
	zep := s.index["zep_wll"]
	full, skim := s.userProfiles().profiles["full"][zep], s.userProfiles().profiles["skim"][zep]
	if skim <= 0 || full/skim < 9.9 || full/skim > 10.1 {
		t.Errorf("full listen %v, a tenth %v: the ratio should be 10", full, skim)
	}
}
