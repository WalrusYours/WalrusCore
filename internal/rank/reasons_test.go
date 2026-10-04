package rank

import (
	"slices"
	"strings"
	"testing"

	"github.com/timurcravtov/walrus/internal/recommend"
	"github.com/timurcravtov/walrus/internal/schema"
)

func TestEachSignalExplainsItselfInTheBreakdown(t *testing.T) {
	f := newFixture(t)
	res := f.ask(recommend.Request{Items: []string{"acdc_bib", "acdc_thunder"}, Limit: 20})

	var ozzy *recommend.Breakdown
	for _, it := range res.Items {
		if it.ID == "ozzy_crazy" {
			b, err := f.svc.ExplainRec(res.RecID, it.ID)
			if err != nil {
				t.Fatal(err)
			}
			ozzy = b
		}
	}
	if ozzy == nil {
		t.Fatalf("ozzy_crazy should be suggested: %v", ids(res))
	}
	because := map[string]string{}
	for _, s := range ozzy.Breakdown {
		because[s.Signal] = s.Because
		if s.Value == 0 && s.Because != "" {
			t.Errorf("%s contributed nothing but explains itself: %q", s.Signal, s.Because)
		}
	}

	if !strings.Contains(because["co_listed"], "Often added to playlists with") || !strings.Contains(because["co_listed"], "Thunderstruck") {
		t.Errorf("co_listed = %q: the schema's own wording, naming the seed song it was seen with", because["co_listed"])
	}
	if g := because["genre_fit"]; !strings.HasPrefix(g, "Shares ") || !strings.Contains(g, "rock") {
		t.Errorf("genre_fit = %q: it should name the genres actually shared", g)
	}
	if s := because["sounds_like"]; !strings.HasPrefix(s, "Close to ") || !strings.Contains(s, " in ") {
		t.Errorf("sounds_like = %q: it should name what is close", s)
	}
	if e := because["energy_fit"]; !strings.Contains(e, "usual energy") || !strings.Contains(e, " against ") {
		t.Errorf("energy_fit = %q: it should give both numbers", e)
	}
}

func TestTheOneLineReasonJoinsOnlyStrongSignals(t *testing.T) {
	why := func(s string) func(int) string { return func(int) string { return s } }
	cols := []column{
		{id: "a", why: why("first")},
		{id: "b", why: why("second")},
		{id: "c", why: why("third")},
		{id: "d", why: why("")},
		{id: "e", why: nil},
	}
	because, reason := explain(cols, 0, []float64{0.5, 0.3, 0.2, 0.1, 0.1})
	if reason != "first · second" {
		t.Errorf("reason = %q: the second joins because it is 60%% of the first, the third is not added", reason)
	}
	if len(because) != 3 || because["c"] != "third" || because["d"] != "" || because["e"] != "" {
		t.Errorf("because = %v: every signal with something to say, and no empty entries", because)
	}

	if _, reason := explain(cols, 0, []float64{0.5, 0.1, 0, 0, 0}); reason != "first" {
		t.Errorf("a weak second signal is left out: %q", reason)
	}
	if _, reason := explain(cols, 0, []float64{0, 0.4, 0.3, 0, 0}); reason != "second · third" {
		t.Errorf("a signal that contributed nothing is skipped: %q", reason)
	}
	if because, reason := explain(cols, 0, []float64{0, 0, 0, 0, 0}); reason != "" || len(because) != 0 {
		t.Errorf("nothing contributed, nothing to say: %q %v", reason, because)
	}
}

func TestSchemaWordingIsUsedOnlyWhenItCanBeFilled(t *testing.T) {
	x := &ranking{snapshot: &snapshot{sch: &schema.Schema{}}}
	spec := schema.SignalSpec{Explain: schema.Text{Plain: "Often with {seed_items} ({count} times)"}}
	if got := x.say(spec, "default", map[string]string{"seed_items": "A", "count": "3"}); got != "Often with A (3 times)" {
		t.Errorf("filled = %q", got)
	}
	if got := x.say(spec, "default {count}", map[string]string{"count": "3"}); got != "default 3" {
		t.Errorf("a placeholder that cannot be filled falls back to the built-in wording: %q", got)
	}
	if got := x.say(schema.SignalSpec{}, "plain", nil); got != "plain" {
		t.Errorf("no schema text = %q", got)
	}
	if got := x.say(schema.SignalSpec{}, "needs {x}", nil); got != "" {
		t.Errorf("nothing that can be completed says nothing: %q", got)
	}
	local := schema.SignalSpec{Explain: schema.Text{Locales: map[string]string{"en": "English", "ro": "Romana"}}}
	if got := x.say(local, "default", nil); got != "English" {
		t.Errorf("locales = %q", got)
	}
}

func TestEmptyPlaylistTitleMatchNamesTheWords(t *testing.T) {
	f := newFixture(t)
	res := f.ask(recommend.Request{Items: []string{}, Limit: 3, Context: map[string]any{"title_words": []any{"folk", "indie"}}})
	if res.Used != "from_title" || len(res.Items) == 0 {
		t.Fatalf("used = %s, items = %v", res.Used, ids(res))
	}
	if r := res.Items[0].Reason; !strings.Contains(r, "from the title") || !strings.Contains(r, "“folk”") {
		t.Errorf("reason = %q", r)
	}
}

func TestWordsHelpers(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{list([]string{"a"}, 3), "a"},
		{list([]string{"a", "b"}, 3), "a and b"},
		{list([]string{"a", "b", "c"}, 3), "a, b and c"},
		{list([]string{"a", "b", "c", "d", "e"}, 3), "a, b and c and 2 more"},
		{list(nil, 3), ""},
		{plural(1, "time"), "1 time"},
		{plural(4, "time"), "4 times"},
		{humanise("artist_id"), "artist"},
		{humanise("release_date"), "release date"},
		{num(0.9), "0.90"},
		{strings.Join(quote([]string{"jazz"}), ""), "“jazz”"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	if !slices.Equal(quote(nil), []string{}) {
		t.Error("quote of nothing is nothing")
	}
}

func TestOnlyUnusuallyCloseFeaturesAreNamed(t *testing.T) {
	// "flat" is about the same for every item, so being close on it means nothing; "pace" varies
	f := newFixture(t)
	_ = f
	x := &snapshot{scales: map[string][2]float64{"flat": {}, "pace": {}, "mood": {}}}
	for i, pace := range []float64{0.1, 0.3, 0.5, 0.7, 0.9, 0.5} {
		x.items = append(x.items, entity(i, map[string]float64{"flat": 0.80 + float64(i)/1000, "pace": pace, "mood": float64(i%2) * 0.9}))
		x.candidates = append(x.candidates, i)
	}
	x.gaps = x.candidateGaps()

	got := x.closest(2, 5, feats("flat", "pace", "mood"), 2) // items 2 and 5 share pace 0.5
	if !slices.Equal(got, []string{"pace"}) {
		t.Errorf("got %v: only pace is both close and unusual (mood differs, flat is close for everyone)", got)
	}
	if far := x.closest(0, 3, feats("flat", "pace", "mood"), 2); len(far) != 0 {
		t.Errorf("a pair that is far apart on what varies names nothing: %v", far)
	}
	if len(x.closest(2, 5, feats("not_an_attribute"), 2)) != 0 {
		t.Error("an attribute nobody has is never named")
	}
}

func feats(attrs ...string) []feature {
	out := make([]feature, len(attrs))
	for i, a := range attrs {
		out[i] = feature{name: a, attr: a}
	}
	return out
}
