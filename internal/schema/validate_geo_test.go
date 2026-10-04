package schema

import "testing"

// Rules of geo points, the inclusive and distance conditions, and $seed references. Each case
// edits a shipped example so exactly one rule is broken.

func TestGeoAndSeedRulesOnLocal(t *testing.T) {
	within := `{ id: within_range,  require: { attribute: location, within_km: 15, of: "$context.location" } }`
	near := `near:         { type: proximity, on: location, to: "$context.location", half_distance: 3, default: 0.5 }`
	runV2Cases(t, "local.yml", []v2Case{
		{"within_km on a number", within, `{ id: within_range,  require: { attribute: rating, within_km: 15, of: "$context.location" } }`, "constraints[1].require.attribute", "needs a geo attribute"},
		{"within_km without a place", within, `{ id: within_range,  require: { attribute: location, within_km: 15 } }`, "constraints[1].require.of", "name the place"},
		{"a place without within_km", within, `{ id: within_range,  require: { attribute: location, equals: x, of: "$context.location" } }`, "constraints[1].require.of", "applies only to within_km"},
		{"the place is not geo", within, `{ id: within_range,  require: { attribute: location, within_km: 15, of: "$context.budget" } }`, "constraints[1].require.of", "not geo"},
		{"the place is not a reference", within, `{ id: within_range,  require: { attribute: location, within_km: 15, of: somewhere } }`, "constraints[1].require.of", "the place is $context"},
		{"a negative distance", within, `{ id: within_range,  require: { attribute: location, within_km: -5, of: "$context.location" } }`, "constraints[1].require.within_km", "greater than 0"},

		{"$seed names no attribute", `equals: "$seed.chain_id"`, `equals: "$seed.owner"`, "constraints[4].require.equals", "not an attribute"},
		{"$seed needs an item seed", "constraints: [open, within_range, near_home, diet_ok, not_for_me]", "constraints: [open, same_chain, within_range, near_home, diet_ok, not_for_me]", "recommenders.nearby.constraints", "refers to $seed"},

		{"proximity on a number", near, `near:         { type: proximity, on: rating, to: "$context.location", half_distance: 3, default: 0.5 }`, "signals.near.on", "needs a geo attribute"},
		{"proximity without a place", near, `near:         { type: proximity, on: location, half_distance: 3, default: 0.5 }`, "signals.near.to", "needs to"},
		{"proximity to a number", near, `near:         { type: proximity, on: location, to: "$context.budget", half_distance: 3, default: 0.5 }`, "signals.near.to", "not geo"},
		{"proximity with no distance", near, `near:         { type: proximity, on: location, to: "$context.location", half_distance: 0, default: 0.5 }`, "signals.near.half_distance", "greater than 0"},

		{"a geo point in a similarity term", "- { id: quality,  on: rating,      metric: closeness, weight: 0.2 }", "- { id: quality,  on: location,    metric: closeness, weight: 0.2 }", "similarity.venue[2].on", "geo point"},
		{"closeness on a set", "- { id: price,    on: price_level, metric: closeness, weight: 0.2 }", "- { id: price,    on: cuisines,    metric: closeness, weight: 0.2 }", "similarity.venue[1].on", "needs a float or int"},
	})
}

func TestInclusiveRangesOnDating(t *testing.T) {
	runV2Cases(t, "dating.yml", []v2Case{
		{"two operators", `require: { attribute: age, gte: "$user.min_age" }`, `require: { attribute: age, gte: "$user.min_age", lte: "$user.max_age" }`, "constraints[2].require", "exactly one of"},
		{"gte reads a missing user attribute", `gte: "$user.min_age"`, `gte: "$user.minimum"`, "constraints[2].require.gte", "not an attribute of user"},
		{"within_km reads the user's place", `of: "$user.location"`, `of: "$user.looking_for"`, "constraints[4].require.of", "not geo"},
	})
}

func TestScopedByTheSeedOnMovies(t *testing.T) {
	runV2Cases(t, "movies.yml", []v2Case{
		{"$seed on an attribute the film lacks", `equals: "$seed.director_id"`, `equals: "$seed.studio"`, "constraints[3].require.equals", "not an attribute"},
		{"a scoped constraint on a user seed", "constraints: [age_ok, not_watched]\n    rules: [boost_originals", "constraints: [age_ok, not_watched, same_director]\n    rules: [boost_originals", "recommenders.home.constraints", "refers to $seed"},
	})
}
