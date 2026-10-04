package rank

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/ingest"
	"github.com/timurcravtov/walrus/internal/recommend"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store/memory"
)

// engine is an empty engine running one of the shipped example schemas.
func engine(t *testing.T, example string) *fixture {
	t.Helper()
	return engineText(t, exampleFile(t, example))
}

func engineText(t *testing.T, text string) *fixture {
	t.Helper()
	sch := schema.NewService()
	if res := sch.Load([]byte(text), schema.LoadOptions{Author: "test"}); !res.OK {
		t.Fatalf("schema rejected: %+v", res.Errors)
	}
	st := memory.New()
	f := &fixture{t: t, sch: sch, ing: ingest.NewService(sch, st), ranks: New(st)}
	f.svc = recommend.NewService(sch, f.ranks).WithHistory(f.ranks)
	return f
}

func (f *fixture) events(events ...ingest.RawInteraction) {
	f.t.Helper()
	for i := range events {
		if events[i].TS == "" {
			events[i].TS = time.Now().UTC().Add(-time.Duration(i+1) * time.Minute).Format(time.RFC3339)
		}
	}
	if res, err := f.ing.Interactions(context.Background(), events); err != nil || len(res.Rejected) > 0 {
		f.t.Fatalf("events: %+v %v", res, err)
	}
}

func (f *fixture) recommend(req recommend.Request) (*recommend.Response, error) {
	req.Explain = true
	return f.svc.Recommend(context.Background(), req)
}

func (f *fixture) must(req recommend.Request) *recommend.Response {
	f.t.Helper()
	res, err := f.recommend(req)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func point(lat, lon float64) map[string]any { return map[string]any{"lat": lat, "lon": lon} }

func sorted(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

// ---- local.yml: nearby

func venue(id, chain string, lat, lon, rating float64, status string) ingest.Raw {
	attrs := map[string]any{
		"name": id, "cuisines": g("pizza"), "diets": g("vegetarian"), "location": point(lat, lon),
		"price_level": 2.0, "rating": rating, "status": status, "opened_at": "2024-01-01T00:00:00Z",
	}
	if chain != "" {
		attrs["chain_id"] = chain
	}
	return ingest.Raw{Entity: "venue", ID: id, Attributes: attrs}
}

func localEngine(t *testing.T) *fixture {
	f := engine(t, "local.yml")
	f.entities([]ingest.Raw{
		{Entity: "chain", ID: "pizza_co", Attributes: map[string]any{"name": "Pizza Co"}},
		venue("centre", "pizza_co", 47.0105, 28.8638, 4.0, "open_for_business"),
		venue("three_km", "", 47.0375, 28.8638, 4.0, "open_for_business"), // 3 km north
		venue("twelve_km", "pizza_co", 47.1185, 28.8638, 4.0, "open_for_business"),
		venue("forty_km", "", 47.37, 28.8638, 4.0, "open_for_business"),
		venue("closed", "", 47.0106, 28.8639, 4.0, "closed"),
	})
	f.events(
		ingest.RawInteraction{User: "p1", Type: "visit", Target: "centre"},
		ingest.RawInteraction{User: "p1", Type: "visit", Target: "three_km"},
		ingest.RawInteraction{User: "p1", Type: "visit", Target: "twelve_km"},
		ingest.RawInteraction{User: "p1", Type: "visit", Target: "forty_km"},
		ingest.RawInteraction{User: "p1", Type: "visit", Target: "closed"},
	)
	return f
}

func TestNearbyOffersOnlyWhatIsWithinRangeAndNearerRanksHigher(t *testing.T) {
	f := localEngine(t)
	res := f.must(recommend.Request{
		Recommender: "popular_nearby", Limit: 20, Context: map[string]any{"location": point(47.0105, 28.8638)},
	})
	got := ids(res)
	if !slices.Equal(sorted(got), []string{"centre", "three_km", "twelve_km"}) {
		t.Fatalf("within 15 km and open: %v", got)
	}
	if !slices.Equal(got, []string{"centre", "three_km", "twelve_km"}) {
		t.Errorf("equal in everything but distance, nearer should come first: %v", got)
	}
	if r := res.Items[0].Reason; !strings.Contains(r, "Under 1 km away") {
		t.Errorf("reason = %q", r)
	}
	if r := res.Items[1].Reason; !strings.Contains(r, "3.0 km away") {
		t.Errorf("reason = %q", r)
	}
}

func TestWithoutAPositionTheRadiusDoesNotApply(t *testing.T) {
	f := localEngine(t)
	got := ids(f.must(recommend.Request{Recommender: "popular_nearby", Limit: 20}))
	if !slices.Contains(got, "forty_km") || slices.Contains(got, "closed") {
		t.Errorf("no location sent: nothing to measure from, but closed places are still excluded: %v", got)
	}
}

func TestAUsersSavedHomeIsAPlaceToo(t *testing.T) {
	f := localEngine(t)
	f.entities([]ingest.Raw{{Entity: "user", ID: "u1", Attributes: map[string]any{"home": point(47.0105, 28.8638), "personalization": true}}})
	f.events(ingest.RawInteraction{User: "u1", Type: "save", Target: "centre"})
	res := f.must(recommend.Request{Recommender: "nearby", User: "u1", Limit: 20})
	if slices.Contains(ids(res), "forty_km") {
		t.Errorf("forty_km is 40 km from home, past the 15 km the schema allows: %v", ids(res))
	}
}

func TestMoreFromTheSameChainUsesTheSeedsOwnAttribute(t *testing.T) {
	f := localEngine(t)
	got := ids(f.must(recommend.Request{Recommender: "more_from_chain", Item: "centre"}))
	if !slices.Equal(got, []string{"twelve_km"}) {
		t.Errorf("only the other Pizza Co branch: %v", got)
	}
	// a seed with no chain has nothing to scope by, so the scope does not apply
	if all := ids(f.must(recommend.Request{Recommender: "more_from_chain", Item: "three_km"})); len(all) < 3 {
		t.Errorf("a venue with no chain should not empty the list: %v", all)
	}
}

// ---- movies.yml: $seed scopes, a set of seeds, explicit ratings

func film(id, director string, year int) ingest.Raw {
	return ingest.Raw{Entity: "film", ID: id, Attributes: map[string]any{
		"title": id, "director_id": director, "genres": g("drama"), "year": float64(year), "runtime_min": 100.0,
		"age_rating": 12.0, "released_at": fmt.Sprintf("%d-01-01T00:00:00Z", year), "original": false,
	}}
}

func moviesEngine(t *testing.T) *fixture {
	f := engine(t, "movies.yml")
	f.entities([]ingest.Raw{
		{Entity: "director", ID: "d1", Attributes: map[string]any{"name": "One"}},
		{Entity: "director", ID: "d2", Attributes: map[string]any{"name": "Two"}},
		{Entity: "director", ID: "d3", Attributes: map[string]any{"name": "Three"}},
		film("a1", "d1", 2001), film("a2", "d1", 2005), film("a3", "d1", 2010),
		film("b1", "d2", 2003), film("b2", "d2", 2008),
		film("c1", "d3", 2012),
	})
	var ev []ingest.RawInteraction
	for _, id := range []string{"a1", "a2", "a3", "b1", "b2", "c1"} {
		ev = append(ev, ingest.RawInteraction{User: "x", Type: "watchlist", Target: id})
	}
	f.events(ev...)
	return f
}

func TestMoreFromThisDirector(t *testing.T) {
	f := moviesEngine(t)
	got := ids(f.must(recommend.Request{Recommender: "more_from_director", Item: "a1"}))
	if !slices.Equal(sorted(got), []string{"a2", "a3"}) {
		t.Errorf("the same director's other films: %v", got)
	}
}

func TestSeveralSeedsScopeToAnyOfTheirValues(t *testing.T) {
	f := moviesEngine(t)
	// scoping by "the director of the seed" with two seeds means either director
	text := strings.Replace(exampleFile(t, "movies.yml"), "seed: item\n    candidates:\n      - { source: popular, cap: 100 }\n    signals: [popularity, recency]\n    constraints: [age_ok, not_in_seed, same_director]",
		"seed: items\n    candidates:\n      - { source: popular, cap: 100 }\n    signals: [popularity, recency]\n    constraints: [age_ok, not_in_seed, same_director]", 1)
	sch := schema.NewService()
	if res := sch.Load([]byte(text), schema.LoadOptions{Author: "test"}); !res.OK {
		t.Fatalf("%+v", res.Errors)
	}
	f.svc = recommend.NewService(sch, f.ranks).WithHistory(f.ranks)
	// the schema service is new, so the catalogue must be pushed into the same store
	f.ing = ingest.NewService(sch, f.ranks.store)
	got := ids(f.must(recommend.Request{Recommender: "more_from_director", Items: []string{"a1", "b1"}}))
	if !slices.Equal(sorted(got), []string{"a2", "a3", "b2"}) {
		t.Errorf("films by either seed director, none of the seeds: %v", got)
	}
}

func exampleFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "schema", "schema-examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestARatingCentresOnThreeStars(t *testing.T) {
	f := moviesEngine(t)
	five, one := 5.0, 1.0
	at := time.Now().UTC().Format(time.RFC3339)
	f.events(
		ingest.RawInteraction{User: "fan", Type: "rate", Target: "a1", Value: &five, TS: at},
		ingest.RawInteraction{User: "hater", Type: "rate", Target: "a1", Value: &one, TS: at},
	)
	c := f.sch.Compiled()
	s, err := f.ranks.build(context.Background(), recommend.RankInput{
		Compiled: c, Recommender: "home", Spec: c.Schema.Recommenders["home"], Seed: recommend.Seed{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	a1 := s.index["a1"]
	profiles := s.userProfiles().profiles
	if profiles["fan"][a1] <= 0 {
		t.Errorf("five stars is liking: %v", profiles["fan"])
	}
	if _, has := profiles["hater"]; has {
		t.Errorf("one star is disliking, not a reason to be similar to people who like it: %v", profiles["hater"])
	}
}

// ---- social.yml: a follow-based feed

func post(id, author string, hoursAgo int) ingest.Raw {
	return ingest.Raw{Entity: "post", ID: id, Attributes: map[string]any{
		"author_id": author, "topics": g("music"), "language": "en", "has_media": false,
		"created_at": time.Now().UTC().Add(-time.Duration(hoursAgo) * time.Hour).Format(time.RFC3339),
	}}
}

func user(id string, following ...string) ingest.Raw {
	f := make([]any, len(following))
	for i, s := range following {
		f[i] = s
	}
	return ingest.Raw{Entity: "user", ID: id, Attributes: map[string]any{
		"handle": id, "following": f, "language": "en", "discoverable": true, "personalization": true,
	}}
}

func socialEngine(t *testing.T) *fixture {
	f := engine(t, "social.yml")
	f.entities([]ingest.Raw{
		user("me", "ann", "bob"), user("ann"), user("bob"), user("cat"),
		post("p_ann_new", "ann", 1), post("p_ann_old", "ann", 30), post("p_bob", "bob", 5), post("p_cat", "cat", 2),
	})
	return f
}

func TestTheFollowingFeedHoldsOnlyWhoIFollowNewestFirst(t *testing.T) {
	f := socialEngine(t)
	got := ids(f.must(recommend.Request{Recommender: "following", User: "me", Limit: 20}))
	if !slices.Equal(got, []string{"p_ann_new", "p_bob", "p_ann_old"}) {
		t.Errorf("posts by ann and bob, newest first, none by cat: %v", got)
	}
}

func TestDiscoverIsTheAccountsIDoNotFollow(t *testing.T) {
	f := socialEngine(t)
	f.events(ingest.RawInteraction{User: "me", Type: "like", Target: "p_cat"})
	f.events(ingest.RawInteraction{User: "other", Type: "like", Target: "p_cat"}, ingest.RawInteraction{User: "other", Type: "like", Target: "p_ann_new"})
	got := ids(f.must(recommend.Request{Recommender: "discover", User: "me", Limit: 20}))
	for _, id := range got {
		if id != "p_cat" {
			t.Errorf("%s is by someone I follow: %v", id, got)
		}
	}
}

func TestHidingAPostRemovesItFromEveryFeed(t *testing.T) {
	f := socialEngine(t)
	f.events(ingest.RawInteraction{User: "me", Type: "hide", Target: "p_bob"})
	got := ids(f.must(recommend.Request{Recommender: "following", User: "me", Limit: 20}))
	if slices.Contains(got, "p_bob") || len(got) != 2 {
		t.Errorf("%v", got)
	}
}

func TestMoreFromThisAuthor(t *testing.T) {
	f := socialEngine(t)
	got := ids(f.must(recommend.Request{Recommender: "more_from_author", Item: "p_ann_new"}))
	if !slices.Equal(got, []string{"p_ann_old"}) {
		t.Errorf("the author's other posts: %v", got)
	}
}

// ---- dating.yml: mutual preferences as ranges and sets

func person(id, gender string, interestedIn []any, age, minAge, maxAge int, lat, lon float64, interests ...string) ingest.Raw {
	in := make([]any, len(interests))
	for i, s := range interests {
		in[i] = s
	}
	return ingest.Raw{Entity: "user", ID: id, Attributes: map[string]any{
		"age": float64(age), "gender": gender, "interested_in": interestedIn, "min_age": float64(minAge), "max_age": float64(maxAge),
		"location": point(lat, lon), "interests": in, "languages": g("en"), "looking_for": "relationship",
		"verified": id == "bob", "response_rate": 0.5, "last_active": time.Now().UTC().Format(time.RFC3339),
		"joined_at": "2024-01-01T00:00:00Z", "status": "active", "personalization": true,
	}}
}

func datingEngine(t *testing.T) *fixture { return datingEngineFrom(t, exampleFile(t, "dating.yml")) }

func datingEngineFrom(t *testing.T, text string) *fixture {
	f := engineText(t, text)
	f.entities([]ingest.Raw{
		person("alice", "f", g("m"), 30, 28, 35, 47.01, 28.86, "hiking", "jazz"),
		person("bob", "m", g("f"), 31, 25, 40, 47.02, 28.87, "hiking", "jazz"),  // fits
		person("carl", "m", g("f"), 40, 25, 40, 47.02, 28.87, "hiking", "jazz"), // too old
		person("dan", "f", g("f"), 30, 25, 40, 47.02, 28.87, "hiking", "jazz"),  // not who alice wants
		person("erin", "m", g("f"), 29, 25, 40, 44.43, 26.10, "hiking", "jazz"), // far away
		person("fay", "m", g("f"), 33, 25, 40, 47.02, 28.87, "hiking", "jazz"),  // blocked
		person("gus", "m", g("f"), 28, 25, 40, 47.02, 28.87, "hiking", "jazz"),  // youngest allowed
		person("hal", "m", g("f"), 27, 25, 40, 47.02, 28.87, "hiking", "jazz"),  // just too young
		person("ivo", "m", g("f"), 35, 25, 40, 47.02, 28.87, "hiking", "jazz"),  // oldest allowed
		person("zed", "m", g("f"), 32, 25, 40, 47.02, 28.87, "hiking", "jazz"),  // alice liked him already
	})
	f.events(
		ingest.RawInteraction{User: "alice", Type: "like", Target: "zed"},
		ingest.RawInteraction{User: "alice", Type: "block", Target: "fay"},
	)
	return f
}

func TestPeopleAreFilteredByMutualPreferences(t *testing.T) {
	f := datingEngine(t)
	got := ids(f.must(recommend.Request{Recommender: "people", User: "alice", Limit: 20}))
	want := []string{"bob", "gus", "ivo"}
	if !slices.Equal(sorted(got), want) {
		t.Errorf("got %v, want %v: men aged 28 to 35 within 100 km, not seen, not blocked", sorted(got), want)
	}
}

func TestAgeRangesAreInclusive(t *testing.T) {
	f := datingEngine(t)
	got := ids(f.must(recommend.Request{Recommender: "people", User: "alice", Limit: 20}))
	if !slices.Contains(got, "gus") || !slices.Contains(got, "ivo") {
		t.Errorf("28 and 35 are inside the range 28 to 35: %v", got)
	}
	if slices.Contains(got, "hal") {
		t.Errorf("27 is outside it: %v", got)
	}
}

func TestVerifiedOnlyIsAHardFilterWhenAsked(t *testing.T) {
	f := datingEngine(t)
	got := ids(f.must(recommend.Request{Recommender: "people", User: "alice", Limit: 20, Context: map[string]any{"verified_only": true}}))
	if !slices.Equal(got, []string{"bob"}) {
		t.Errorf("only bob is verified: %v", got)
	}
}

func TestWhatThePlatformKnowsCanBeSentAsScores(t *testing.T) {
	f := datingEngine(t)
	plain := ids(f.must(recommend.Request{Recommender: "people", User: "alice", Limit: 20}))
	liked := ids(f.must(recommend.Request{
		Recommender: "people", User: "alice", Limit: 20, Scores: map[string]map[string]float64{"liked_you": {"ivo": 1}},
	}))
	if liked[0] != "ivo" {
		t.Errorf("ivo liked alice, so he should lead: %v (without: %v)", liked, plain)
	}
}

func TestSensitiveAttributesDoNotAppearInReasons(t *testing.T) {
	f := datingEngine(t)
	for _, it := range f.must(recommend.Request{Recommender: "people", User: "alice", Limit: 20}).Items {
		for _, word := range []string{"gender", "interested"} {
			if strings.Contains(strings.ToLower(it.Reason), word) {
				t.Errorf("%s: %q", it.ID, it.Reason)
			}
		}
	}
}

func TestSensitiveAttributesAreScoredButNeverNamed(t *testing.T) {
	text := exampleFile(t, "dating.yml")
	marked := strings.Replace(text, "interests:     { type: set, of: string }", "interests:     { type: set, of: string, sensitive: true }", 1)
	if marked == text {
		t.Fatal("test setup: interests not found")
	}
	f := datingEngineFrom(t, marked)
	res := f.must(recommend.Request{Recommender: "people", User: "alice", Limit: 20})
	if len(res.Items) == 0 {
		t.Fatal("no suggestions")
	}
	for _, it := range res.Items {
		b, err := f.svc.ExplainRec(res.RecID, it.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range append([]recommend.SignalShare{{Because: it.Reason}}, b.Breakdown...) {
			if strings.Contains(row.Because, "hiking") || strings.Contains(row.Because, "jazz") {
				t.Errorf("%s: a sensitive attribute was named: %q", it.ID, row.Because)
			}
		}
	}

	// the same engine with the attribute not sensitive does name what two people share
	open := datingEngineFrom(t, text)
	named := false
	for _, it := range open.must(recommend.Request{Recommender: "people", User: "alice", Limit: 20}).Items {
		named = named || strings.Contains(it.Reason, "hiking") || strings.Contains(it.Reason, "jazz")
	}
	if !named {
		t.Error("control: a shared interest should be named when it is not sensitive")
	}
}
