package api

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/recommend"
)

// The whole path over HTTP on the music schema: push the schema, send the library and other
// people's playlists, then ask for "you might also add" with different Tune values. The tables in
// the log (go test -v) are the point.
func TestMusicRecommendationFromTheStore(t *testing.T) {
	srv := newTestServer(t)
	pushExample(t, srv, "spotify.yml")

	byID := map[string]song{}
	var entities []string
	for _, s := range catalogue {
		byID[s.id] = s
		genres, _ := json.Marshal(s.genres)
		entities = append(entities, fmt.Sprintf(
			`{"entity":"track","id":%q,"attributes":{"title":%q,"artist_id":%q,"album_id":%q,"genres":%s,"language":"en",
"explicit":false,"release_date":"1980-01-01T00:00:00Z","duration_ms":240000,"available_in":["MD","US"],
"energy":%v,"valence":%v,"danceability":%v,"acousticness":%v,"tempo":%v}}`,
			s.id, s.title, strings.ToLower(s.artist), strings.ToLower(s.artist)+"_album", genres,
			s.energy, s.valence, math.Round(s.valence*80)/100, math.Round((1-s.energy)*100)/100, math.Round(80+80*s.energy)))
	}
	if res, raw := do(t, srv, "POST", "/v1/entities", `{"entities":[`+strings.Join(entities, ",")+`]}`, bearer); res.StatusCode != 200 || !strings.Contains(string(raw), `"rejected":[]`) {
		t.Fatalf("entities: %d %s", res.StatusCode, raw)
	}

	var events []string
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for p, tracks := range playlists {
		for k, id := range tracks {
			events = append(events, fmt.Sprintf(`{"user":"u%d","type":"add_to_playlist","target":%q,"ts":%q,"fields":{"playlist_id":"p%d"}}`,
				p, id, at.Add(time.Duration(k)*time.Minute).Format(time.RFC3339), p))
		}
	}
	if res, raw := do(t, srv, "POST", "/v1/interactions", `{"interactions":[`+strings.Join(events, ",")+`]}`, bearer); res.StatusCode != 200 || !strings.Contains(string(raw), `"rejected":[]`) {
		t.Fatalf("interactions: %d %s", res.StatusCode, raw)
	}

	line := func(s song) string {
		return fmt.Sprintf("%-24s %-14s %-22s energy %.2f  valence %.2f", s.title, s.artist, strings.Join(s.genres, ", "), s.energy, s.valence)
	}
	seed := []string{"back_in_black", "thunderstruck", "enter_sandman"}
	t.Log(`PLAYLIST "Garage rock night":`)
	for _, id := range seed {
		t.Log("   " + line(byID[id]))
	}

	ask := func(label, extra string) recommend.Response {
		t.Helper()
		body := `{"items":["` + strings.Join(seed, `","`) + `"],"limit":5,"explain":true` + extra + `}`
		res, raw := do(t, srv, "POST", "/v1/recommenders/playlist_add/recommend", body, bearer)
		if res.StatusCode != 200 {
			t.Fatalf("%d %s", res.StatusCode, raw)
		}
		var r recommend.Response
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		t.Logf("\n%s\n   candidates considered: %d", label, r.CandidatesConsidered)
		for i, it := range r.Items {
			t.Logf("   %d. %.3f  %s\n         %s", i+1, it.Score, line(byID[it.ID]), it.Reason)
		}
		return r
	}

	base := ask("DEFAULT TUNE", "")
	branch := ask(`TUNE: vibe_vs_branch_out = 1 ("branch out")`, `,"knobs":{"vibe_vs_branch_out":1}`)
	hits := ask(`TUNE: deep_cuts_vs_hits = 1 ("hits")`, `,"knobs":{"deep_cuts_vs_hits":1}`)

	if len(base.Items) != 5 {
		t.Fatalf("items = %d", len(base.Items))
	}
	for _, r := range []recommend.Response{base, branch, hits} {
		for _, it := range r.Items {
			if slices.Contains(seed, it.ID) {
				t.Errorf("%s is already in the playlist", it.ID)
			}
		}
	}
	for _, it := range base.Items[:3] {
		g := strings.Join(byID[it.ID].genres, " ")
		if !strings.Contains(g, "rock") && !strings.Contains(g, "metal") {
			t.Errorf("a rock and metal playlist should be suggested rock and metal first: %+v", base.Items)
		}
	}
	if base.Items[0].Reason == "" {
		t.Error("the first suggestion should say why")
	}
	if slices.Equal(idsOf(base), idsOf(branch)) && slices.Equal(idsOf(base), idsOf(hits)) {
		t.Error("moving the Tune sliders should change the list")
	}

	res, raw := do(t, srv, "GET", "/v1/recommendations/"+base.RecID+"/explain/"+base.Items[0].ID, "", bearer)
	var b recommend.Breakdown
	_ = json.Unmarshal(raw, &b)
	sum := 0.0
	t.Logf("\nWHY %q:", byID[b.Item].title)
	for _, s := range b.Breakdown {
		t.Logf("   %-12s %.3f", s.Signal, s.Value)
		sum += s.Value
	}
	if res.StatusCode != 200 || math.Abs(sum-b.Score) > 1e-9 {
		t.Errorf("explain = %d, breakdown sums to %v, score %v", res.StatusCode, sum, b.Score)
	}
}

func idsOf(r recommend.Response) []string {
	out := make([]string, len(r.Items))
	for i, it := range r.Items {
		out[i] = it.ID
	}
	return out
}
