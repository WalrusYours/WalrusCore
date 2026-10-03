package recommend

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestBucketIsDeterministicAndSpread(t *testing.T) {
	if bucket("layer:x", "mara") != bucket("layer:x", "mara") {
		t.Fatal("the same unit must always get the same bucket")
	}
	if bucket("layer:x", "mara") == bucket("layer:y", "mara") && bucket("layer:x", "kofi") == bucket("layer:y", "kofi") {
		t.Error("different salts should reshuffle units (layers are independent)")
	}
	var hist [10]int
	for i := 0; i < 20_000; i++ {
		hist[bucket("s", fmt.Sprint("user", i))/(buckets/10)]++
	}
	for d, n := range hist {
		if n < 1700 || n > 2300 {
			t.Errorf("decile %d holds %d of 20000 units; buckets are not uniform: %v", d, n, hist)
		}
	}
}

// shop.yml has a running experiment on `home`, 50% of traffic, control and more_trend 50/50, with
// an audience on $user.country that cannot be met before the store exists, and a 5% holdout.
func shopService(t *testing.T, keepAudience bool) (*Service, *fakeRanker) {
	t.Helper()
	text := exampleText(t, "shop.yml")
	if !keepAudience {
		audience := "    audience: { when: \"$user.country == 'MD'\" }\n"
		if !strings.Contains(text, audience) {
			t.Fatal("test setup: audience line not found")
		}
		text = strings.Replace(text, audience, "", 1)
	}
	f := &fakeRanker{items: scored("l1")}
	return newService(t, text, f), f
}

func askHome(s *Service, user string) (*Response, error) {
	return s.Recommend(context.Background(), Request{
		Recommender: "home", User: user, Context: map[string]any{"device": "phone"},
	})
}

func TestExperimentAssignment(t *testing.T) {
	s, f := shopService(t, false)

	const users = 6000
	var enrolled, control, trend, held int
	first := map[string]string{}
	for i := 0; i < users; i++ {
		u := fmt.Sprint("user", i)
		r, err := askHome(s, u)
		if err != nil {
			t.Fatal(code(err))
		}
		switch {
		case r.Holdout:
			held++
			if r.Experiment != nil || r.Used != "popular_now" {
				t.Fatalf("a held-out user is in no experiment and gets the holdout recommender: %+v", r)
			}
		case r.Experiment == nil:
		default:
			enrolled++
			first[u] = r.Experiment.Variant
			if r.Experiment.ID != "trend_weight" {
				t.Fatalf("experiment = %+v", r.Experiment)
			}
			if r.Experiment.Variant == "control" {
				control++
				// the knob's default (0.5 of the choice) gives trending 0.4 * 0.5
				if !near(f.got.Weights["trending"], 0.2) {
					t.Fatalf("control trending weight = %v, want 0.2", f.got.Weights["trending"])
				}
			} else {
				trend++
				// the variant moves trend_strength's default to 1: trending 0.4 * 1
				if !near(f.got.Weights["trending"], 0.4) {
					t.Fatalf("variant trending weight = %v, want 0.4", f.got.Weights["trending"])
				}
			}
		}
	}

	// 5% holdout first, then half of the rest are enrolled, split evenly
	if held < 230 || held > 370 {
		t.Errorf("holdout took %d of %d, want about 300 (5%%)", held, users)
	}
	if share := float64(enrolled) / float64(users-held); share < 0.46 || share > 0.54 {
		t.Errorf("%.1f%% of the eligible users were enrolled, want about 50%%", share*100)
	}
	if split := float64(trend) / float64(enrolled); split < 0.45 || split > 0.55 {
		t.Errorf("%d control / %d variant is not an even split", control, trend)
	}

	// the same user always lands in the same variant
	for u, want := range first {
		r, _ := askHome(s, u)
		if r.Experiment == nil || r.Experiment.Variant != want {
			t.Fatalf("%s changed variant: %+v, was %s", u, r.Experiment, want)
		}
		if len(first) > 50 {
			break
		}
	}
}

func TestAudienceNotMetMeansNotEnrolled(t *testing.T) {
	s, _ := shopService(t, true) // $user.country is unknown until the store exists
	for i := 0; i < 500; i++ {
		r, err := askHome(s, fmt.Sprint("user", i))
		if err != nil {
			t.Fatal(code(err))
		}
		if r.Experiment != nil {
			t.Fatalf("user%d was enrolled although the audience cannot be met: %+v", i, r.Experiment)
		}
	}
}

func TestNoUserNoAssignment(t *testing.T) {
	s, _ := shopService(t, false)
	// a user-unit experiment needs a user; a request without one is served the base schema
	r, err := s.Recommend(context.Background(), Request{Recommender: "popular_now", Context: map[string]any{"device": "phone"}})
	if err != nil {
		t.Fatal(code(err))
	}
	if r.Experiment != nil || r.Holdout {
		t.Errorf("no user, no assignment: %+v", r)
	}
}

func TestAUserKnobBeatsTheVariant(t *testing.T) {
	s, f := shopService(t, false)
	// find a user in the variant, then let them move the knob: their value wins
	for i := 0; i < 500; i++ {
		u := fmt.Sprint("user", i)
		r, _ := askHome(s, u)
		if r.Experiment == nil || r.Experiment.Variant != "more_trend" {
			continue
		}
		r, err := s.Recommend(context.Background(), Request{
			Recommender: "home", User: u, Context: map[string]any{"device": "phone"},
			Knobs: map[string]float64{"trend_strength": 0},
		})
		if err != nil {
			t.Fatal(code(err))
		}
		if !near(f.got.Weights["trending"], 0) || !near(r.Knobs["trend_strength"], 0) {
			t.Fatalf("the user's own value must win over the variant's default: %v", f.got.Weights["trending"])
		}
		return
	}
	t.Fatal("found no user in the variant")
}
