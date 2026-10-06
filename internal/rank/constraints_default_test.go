package rank

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/timurcravtov/walrus/internal/ingest"
	"github.com/timurcravtov/walrus/internal/recommend"
)

const constraintSchema = `version: 1
meta: { name: Reel, locales: [en], default_locale: en }
entities:
  user: { key: id }
  film: { key: id, attributes: { title: { type: string } } }
interactions:
  watch: { kind: positive, weight: 2 }
signals:
  popularity: { type: global_count, default: 0.5 }
constraints:
  - { id: not_watched, exclude: { interacted: [watch], count_gte: 1 } }
recommenders:
  omits:
    for: [film]
    seed: user
    signals: [popularity]
  lists:
    for: [film]
    seed: user
    signals: [popularity]
    constraints: [not_watched]
  opts_out:
    for: [film]
    seed: user
    signals: [popularity]
    constraints: []
recommendable: film
`

func constraintEngine(t *testing.T, text string) *fixture {
	t.Helper()
	f := engineText(t, text)
	var films []ingest.Raw
	for i := 1; i <= 4; i++ {
		films = append(films, ingest.Raw{Entity: "film", ID: fmt.Sprintf("f%d", i), Attributes: map[string]any{"title": fmt.Sprintf("Film %d", i)}})
	}
	f.entities(films)
	f.events(
		ingest.RawInteraction{User: "ann", Type: "watch", Target: "f1"},
		ingest.RawInteraction{User: "bob", Type: "watch", Target: "f2"},
		ingest.RawInteraction{User: "bob", Type: "watch", Target: "f3"},
	)
	return f
}

// A hard filter must not be skipped by leaving it out of a recommender: the schema's own spec says an
// unlisted `constraints` means every constraint. Only an explicit empty list opts out.
func TestARecommenderThatListsNoConstraintsUsesAllOfThem(t *testing.T) {
	f := constraintEngine(t, constraintSchema)
	for name, wantSeen := range map[string]bool{"omits": false, "lists": false, "opts_out": true} {
		got := ids(f.must(recommend.Request{Recommender: name, User: "ann", Limit: 10}))
		if seen := slices.Contains(got, "f1"); seen != wantSeen {
			t.Errorf("%s: the film ann already watched is in the list = %v, want %v (%v)", name, seen, wantSeen, got)
		}
		if !slices.Contains(got, "f2") || !slices.Contains(got, "f4") {
			t.Errorf("%s: films she has not watched must still be offered: %v", name, got)
		}
	}
}

// With no `recommenders` section the implicit `default` recommender serves the v1 route; it must
// apply the schema's constraints too.
func TestTheImplicitDefaultRecommenderAppliesTheSchemasConstraints(t *testing.T) {
	_, withoutRecommenders, _ := strings.Cut(constraintSchema, "recommenders:")
	text := strings.Replace(constraintSchema, "recommenders:"+strings.Split(withoutRecommenders, "recommendable:")[0], "", 1)
	if strings.Contains(text, "omits:") {
		t.Fatal("test setup: the recommenders section was not removed")
	}
	f := constraintEngine(t, text)
	got := ids(f.must(recommend.Request{Recommender: "default", User: "ann", Limit: 10}))
	if slices.Contains(got, "f1") {
		t.Errorf("the default recommender offered a film the user watched, which the schema forbids: %v", got)
	}
	if len(got) != 3 {
		t.Errorf("the other three films should be offered: %v", got)
	}
	// and a user with nothing watched loses nothing
	if all := ids(f.must(recommend.Request{Recommender: "default", User: "nobody", Limit: 10})); len(all) != 4 {
		t.Errorf("a user with no history should see every film: %v", all)
	}
}
