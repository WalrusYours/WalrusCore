package schema

import (
	"strings"
	"testing"
)

// v2Case edits one example so exactly one rule is broken, and expects an issue at path whose
// message contains the given text. Every rule of SCHEMA-V2.md Appendix A has a case here.
type v2Case struct {
	name, old, new, path, contains string
}

func runV2Cases(t *testing.T, file string, cases []v2Case) {
	t.Helper()
	base := exampleText(t, file)
	if issues := issuesFor(t, base); len(issues) != 0 {
		t.Fatalf("%s must be valid before editing it: %v", file, issues)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(base, c.old) {
				t.Fatalf("test setup: %q not found in %s", c.old, file)
			}
			issues := issuesFor(t, strings.Replace(base, c.old, c.new, 1))
			for _, is := range issues {
				if is.Path == c.path && strings.Contains(is.Message, c.contains) {
					return
				}
			}
			t.Errorf("no issue at %q containing %q; got %v", c.path, c.contains, issues)
		})
	}
}

func TestV2ShopRules(t *testing.T) {
	runV2Cases(t, "shop.yml", []v2Case{
		// meta and text
		{"bad locale", "locales: [en, ro, ru]", "locales: [en, romanian, ru]", "meta.locales[1]", "not a locale"},
		{"default locale not listed", "default_locale: en", "default_locale: fr", "meta.default_locale", "not in meta.locales"},
		{"text without the default locale", "label: { en: Taste vs crowd, ro: Gust sau mulțime, ru: Вкус или большинство }", "label: { ro: Gust sau mulțime, ru: Вкус или большинство }", "knobs[0].label", "default locale"},

		// entities
		{"parent is not a self ref", "parent: parent_id", "parent: recurrence_days", "entities.category.parent", "pointing to category"},
		{"lifecycle created not a timestamp", "created: created_at", "created: price", "entities.listing.lifecycle.created", "timestamp"},
		{"lifecycle active without values", "active: { attribute: status, in: [active] }", "active: { attribute: status, in: [] }", "entities.listing.lifecycle.active.in", "active"},
		{"reserved entity name", "  seller:\n    key: id", "  cross:\n    key: id", "entities.cross", "reserved"},

		// interactions
		{"unknown kind", "impression: { kind: exposure,", "impression: { kind: shown,", "interactions.impression.kind", "kind must be"},
		{"exposure with a weight", "impression: { kind: exposure, target: listing, retention: 30d }", "impression: { kind: exposure, weight: 1, target: listing, retention: 30d }", "interactions.impression", "what was shown"},
		{"negative kind with positive weight", "hide:       { kind: negative, weight: -5.0", "hide:       { kind: negative, weight: 5.0", "interactions.hide.weight", "below 0"},
		{"negative interaction fulfils", "hide:       { kind: negative, weight: -5.0, half_life: 90d, dedupe: per_item }", "hide:       { kind: negative, weight: -5.0, half_life: 90d, dedupe: per_item, fulfils: true }", "interactions.hide.fulfils", "cannot fulfil"},
		{"unknown dedupe", "half_life: 30d, dedupe: per_item }", "half_life: 30d, dedupe: per_week }", "interactions.favourite.dedupe", "dedupe must be"},
		{"field ref to nothing", "fields: { cart_id: { type: string } }", "fields: { cart_id: { type: ref, entity: cart } }", "interactions.add_to_cart.fields.cart_id.entity", "declared entity"},

		// context
		{"derived field not an int", "hour:   { type: int, derive: hour }", "hour:   { type: categorical, derive: hour }", "context.hour.type", "derived field is an int"},
		{"unknown derivation", "derive: hour }", "derive: minute }", "context.hour.derive", "derive must be"},
		{"values on a set", "pinned: { type: set, of: string, optional: true }", "pinned: { type: set, of: string, values: [a], optional: true }", "context.pinned.values", "categorical"},

		// similarity
		{"duplicate term id", "- { id: tags,     on: tags,", "- { id: category, on: tags,", "similarity.listing[1].id", "duplicate term id"},
		{"hnsw on a non-vector", "on: price,    metric: log_ratio, weight: 0.10 }", "on: price,    metric: log_ratio, weight: 0.10, index: hnsw }", "similarity.listing[3].index", "vector"},
		{"user keys on an attribute term", "metric: jaccard, weight: 0.35 }", "metric: jaccard, weight: 0.35, min_shared: 2 }", "similarity.listing[1]", "only to via"},
		{"negative interaction defines similarity", "of: [favourite, add_to_cart, purchase]", "of: [favourite, hide]", "similarity.user[0].of[1]", "only positive"},

		// signals: common keys
		{"unknown signal key", "content:         { type: item_neighbors, default: 0.5,", "content:         { type: item_neighbors, windw: 3d, default: 0.5,", "signals.content.windw", "unknown key"},
		{"unknown normalisation", "content:         { type: item_neighbors, default: 0.5,", "content:         { type: item_neighbors, normalise: max, default: 0.5,", "signals.content.normalise", "normalise must be"},
		{"cap above 1", "content:         { type: item_neighbors, default: 0.5,", "content:         { type: item_neighbors, cap: 1.5, default: 0.5,", "signals.content.cap", "(0, 1]"},
		{"history from an untargeted entity", "content:         { type: item_neighbors, default: 0.5,", "content:         { type: item_neighbors, from: [seller], default: 0.5,", "signals.content.from", "no interaction targets seller"},

		// signals: type keys
		{"min_neighbors zero", "min_neighbors: 2", "min_neighbors: 0", "signals.people_like_you.min_neighbors", "whole number"},
		{"low_exposure of a non-exposure", "type: low_exposure, of: [impression]", "type: low_exposure, of: [view]", "signals.exploration.of[0]", "exposure"},
		{"age without on or lifecycle", "    lifecycle:\n      created: created_at\n      active:", "    lifecycle:\n      active:", "signals.recency.on", "lifecycle.created"},
		{"group_by an unknown field", "group_by: cart_id", "group_by: basket_id", "signals.bought_together.group_by", "no field"},
		{"unknown order", "order: after", "order: before", "signals.bought_after.order", "any or after"},
		{"unknown measure", "measure: lift, default: 0.4", "measure: magic, default: 0.4", "signals.bought_after.measure", "lift, cosine or count"},
		{"formula with an unknown attribute", "$item.sponsored, 0, 1)", "$item.sponsord, 0, 1)", "signals.evening_mood.expr", "not an attribute"},
		{"formula with an unknown namespace", "if($context.hour >= 19", "if($session.hour >= 19", "signals.evening_mood.expr", "cannot be used here"},
		{"satiation of a non-fulfilling interaction", "of: [purchase, owned], by: category", "of: [purchase, view], by: category", "signals.already_have.of[1]", "fulfils"},
		{"recurrence signal without the section", "recurrence:\n  of: [purchase, owned]\n  group_by: category\n  learn: { window: 2y, min_users: 50 }\n  never_below: 0.10\n  from: category.recurrence_days\n", "", "signals.restock", "recurrence section"},

		// constraints
		{"in_seed with an attribute", "exclude: { in_seed: true } }", "exclude: { in_seed: true, attribute: status, equals: x } }", "constraints[3].exclude.in_seed", "stands alone"},
		{"duplicate constraint id", "- { id: not_bought,", "- { id: not_hidden,", "constraints[2].id", "duplicate"},

		// rules
		{"two actions", "- { id: seller_cap,     quota:", "- { id: seller_cap,     bury: true, quota:", "rules[1]", "exactly one"},
		{"positions not ascending", "place: { at: [3, 9] }", "place: { at: [9, 3] }", "rules[0].place.at", "go up"},
		{"cap quota with per below max", "max: 2, per: 10", "max: 2, per: 1", "rules[1].quota", "per ≥ max"},
		{"share quota above 1", "min_share: 0.2", "min_share: 1.5", "rules[2].quota", "min_share in (0, 1]"},
		{"cap on a non-exposure", "cap: { exposure: impression", "cap: { exposure: view", "rules[3].cap.exposure", "kind: exposure"},
		{"pin from a non-set field", "pin: { ids: \"$context.pinned\"", "pin: { ids: \"$context.device\"", "rules[4].pin.ids", "set field"},
		{"rule condition on an unknown attribute", "when: \"$item.sponsored\"", "when: \"$item.promoted\"", "rules[0].when", "not an attribute"},
		{"duplicate rule id", "- { id: fresh_share,", "- { id: seller_cap,", "rules[2].id", "duplicate"},

		// knobs
		{"unknown kind", "kind: toggle\n    group:", "kind: switch\n    group:", "knobs[2].kind", "slider, toggle or choice"},
		{"toggle with a range", "kind: toggle\n    group:", "kind: toggle\n    range: [0, 2]\n    group:", "knobs[2].range", "toggle is 0 or 1"},
		{"choice with one option", "      - { value: 0.5, label: { en: Some,  ro: Puține,  ru: Немного } }\n      - { value: 1,   label: { en: Plenty, ro: Multe,  ru: Много } }\n", "", "knobs[3].options", "at least two"},
		{"choice default not an option", "    default: 0.5\n    maps: { trending: \"0.4 * x\" }", "    default: 0.25\n    maps: { trending: \"0.4 * x\" }", "knobs[3].default", "option values"},
		{"preset value not an option", "default:  { taste_vs_crowd: 0.5, trend_strength: 0.5 }", "default:  { taste_vs_crowd: 0.5, trend_strength: 0.3 }", "presets.default.trend_strength", "option values"},
		{"default outside the range", "    range: [0, 1]\n    default: 0.5\n    maps: { content:", "    range: [0, 1]\n    default: 2\n    maps: { content:", "knobs[0].default", "outside"},
		{"scope names nothing", "scope: [home, related]", "scope: [home, homepage]", "knobs[4].scope", "not a declared recommender"},
		{"depends on itself", "    scope: [home]\n    maps: { recommenders.home.diversity: \"x\" }", "    scope: [home]\n    depends_on: diversity\n    maps: { recommenders.home.diversity: \"x\" }", "knobs[6].depends_on", "another declared knob"},
		{"target is precompute", "maps: { trending: \"0.4 * x\" }", "maps: { signals.trending.window: \"x\" }", "knobs[3].maps.signals.trending.window", "T1"},
		{"target is stored data", "maps: { trending: \"0.4 * x\" }", "maps: { interactions.view.weight: \"x\" }", "knobs[3].maps.interactions.view.weight", "T0"},
		{"target an unknown term", "similarity.listing.tags.weight:", "similarity.listing.colour.weight:", "knobs[4].maps.similarity.listing.colour.weight", "not a similarity term"},
		{"half-life scale without a half-life", "interactions.view.half_life_scale", "interactions.owned.half_life_scale", "knobs[5].maps.interactions.owned.half_life_scale", "no half_life"},
		{"diversity of a recommender without rerank", "maps: { recommenders.home.diversity: \"x\" }", "maps: { recommenders.related.diversity: \"x\" }", "knobs[6].maps.recommenders.related.diversity", "no rerank.diversity"},
		{"seed aggregate of a user seed", "maps: { recommenders.home.diversity: \"x\" }", "maps: { recommenders.home.seed_aggregate: \"x\" }", "knobs[6].maps.recommenders.home.seed_aggregate", "items and session"},
		{"strength of a rule without one", "maps: { recommenders.home.diversity: \"x\" }", "maps: { rules.frequency_cap.strength: \"x\" }", "knobs[6].maps.rules.frequency_cap.strength", "no boost, bury or quota"},

		// recommenders
		{"no for", "    for: [listing]\n    seed: user\n    candidates:", "    seed: user\n    candidates:", "recommenders.home.for", "required"},
		{"unknown seed", "seed: items", "seed: list", "recommenders.basket.seed", "seed must be"},
		{"seed aggregate on an item seed", "    seed: item\n    blend_user: 0.2", "    seed: item\n    seed_aggregate: max\n    blend_user: 0.2", "recommenders.related.seed_aggregate", "items and session"},
		{"blend_user on a user seed", "    seed: user\n    candidates:", "    seed: user\n    blend_user: 0.2\n    candidates:", "recommenders.home.blend_user", "item, items and session"},
		{"trend source without a signal", "{ source: trend, signal: trending, cap: 50 }", "{ source: trend, cap: 50 }", "recommenders.home.candidates[2].signal", "needs signal"},
		{"source signal of the wrong type", "{ source: trend, signal: trending, cap: 50 }", "{ source: trend, signal: popularity, cap: 50 }", "recommenders.home.candidates[2].signal", "a trend signal"},
		{"unknown source", "{ source: fresh, cap: 50 }", "{ source: newest, cap: 50 }", "recommenders.home.candidates[3].source", "source must be"},
		{"co-occurrence with no seed", "    seed: none\n    signals: [trending, popularity, recency]", "    seed: none\n    signals: [trending, popularity, recency, bought_after]", "recommenders.popular_now.signals", "start from"},
		{"weight for an unused signal", "weights: { trending: 0.2 }", "weights: { trending: 0.2, bought_together: 0.3 }", "recommenders.home.weights.bought_together", "this recommender uses"},
		{"seed in a user-seeded weight", "weights: { trending: 0.2 }", "weights: { trending: \"0.2 * seed.size\" }", "recommenders.home.weights.trending", "cannot be used here"},
		{"unknown constraint id", "constraints: [active, not_hidden, not_bought]", "constraints: [active, not_hidden, not_sold]", "recommenders.home.constraints", "not the id of a constraint"},
		{"slot past the limit", "limit: { default: 30, max: 100 }", "limit: { default: 5, max: 8 }", "recommenders.home.rules", "past this recommender's limit"},
		{"limit max below default", "limit: { default: 12, max: 30 }", "limit: { default: 12, max: 6 }", "recommenders.related.limit", "max ≥ default"},
		{"lambda above 1", "lambda: 0.3", "lambda: 1.3", "recommenders.home.rerank.diversity.lambda", "[0, 1]"},
		{"fallback cycle", "    constraints: [active]\n    rules: [seller_cap]\n", "    constraints: [active]\n    rules: [seller_cap]\n    fallback:\n      - { when: \"profile.size == 0\", use: home }\n", "recommenders.home.fallback", "cycle"},
		{"fallback to itself", "use: popular_now }", "use: home }", "recommenders.home.fallback[0].use", "itself"},

		// metrics
		{"denominator not an exposure", "ctr:          { ratio: [view, impression] }", "ctr:          { ratio: [view, favourite] }", "metrics.ctr.ratio", "kind: exposure"},
		{"mean of an interaction without a value", "mean: view.value", "mean: favourite.value", "metrics.dwell.mean", "carries a value"},
		{"quantile out of range", "quantile: 0.95", "quantile: 1.5", "metrics.latency_p95.quantile", "(0, 1)"},
		{"unknown list metric", "list: ild", "list: spread", "metrics.diversity.list", "list must be"},
		{"two forms", "returning_users: 7d }", "returning_users: 7d, list: ild }", "metrics.retention_7d", "exactly one"},

		// experiments
		{"no control", "      control: { share: 0.5 }\n      more_trend:", "      baseline: { share: 0.5 }\n      more_trend:", "experiments.trend_weight.variants", "control"},
		{"shares not summing to 1", "      more_trend:\n        share: 0.5", "      more_trend:\n        share: 0.4", "experiments.trend_weight.variants", "sum to 1"},
		{"variant changes precompute", "          rules.seller_cap.quota.max: 3", "          rules.seller_cap.quota.max: 3\n          signals.trending.window: 3h", "experiments.trend_weight.variants.more_trend.set", "T1"},
		{"variant changes stored data", "          rules.seller_cap.quota.max: 3", "          rules.seller_cap.quota.max: 3\n          entities.listing.key: uid", "experiments.trend_weight.variants.more_trend.set", "T0"},
		{"variant changes metrics", "          rules.seller_cap.quota.max: 3", "          rules.seller_cap.quota.max: 3\n          metrics.ctr.ratio: [view, impression]", "experiments.trend_weight.variants.more_trend.set", "cannot change this section"},
		{"variant removes a constraint", "          rules.seller_cap.quota.max: 3", "          rules.seller_cap.quota.max: 3\n          constraints.not_hidden: null", "experiments.trend_weight.variants.more_trend.set", "never remove"},
		{"variant breaks the schema", "recommenders.home.weights.trending: 0.4", "recommenders.home.weights.nope: 0.4", "experiments.trend_weight.variants.more_trend.set", "after applying the variant, recommenders.home.weights.nope"},
		{"layer over full", "status: draft\n    layer: interface\n    traffic: 0.2", "status: running\n    layer: ranking\n    traffic: 0.6", "experiments", "layer ranking"},
		{"unknown goal metric", "goal: { metric: conversion,", "goal: { metric: sales,", "experiments.trend_weight.goal.metric", "not a declared metric"},
		{"guardrail with two limits", "- { metric: latency_p95, max: 50 }", "- { metric: latency_p95, max: 50, max_increase: 0.1 }", "experiments.trend_weight.guardrails[1]", "exactly one"},
		{"unknown method", "method: ab\n    variants:\n      control: { share: 0.5 }\n      more_trend:", "method: abn\n    variants:\n      control: { share: 0.5 }\n      more_trend:", "experiments.trend_weight.method", "method must be"},
		{"audience on an unknown attribute", "$user.country == 'MD'", "$user.nation == 'MD'", "experiments.trend_weight.audience.when", "not an attribute of user"},
		{"unknown gate metric", "metric: ndcg, max_drop", "metric: mrr, max_drop", "experiments.trend_weight.gate.offline.metric", "metric must be"},

		// holdout, evaluation, privacy
		{"personalised holdout", "holdout: { share: 0.05, recommender: popular_now }", "holdout: { share: 0.05, recommender: home }", "holdout.recommender", "seed: none"},
		{"holdout too large", "holdout: { share: 0.05,", "holdout: { share: 0.6,", "holdout.share", "(0, 0.5]"},
		{"unknown offline metric", "metrics: [ndcg, recall, ild, coverage, novelty, gini]", "metrics: [ndcg, mrr]", "evaluation.metrics[1]", "metric must be"},
		{"grid outside the range", "grid: { taste_vs_crowd: [0, 0.5, 1]", "grid: { taste_vs_crowd: [0, 0.5, 2]", "evaluation.grid.taste_vs_crowd", "outside"},
		{"personalised opt-out", "equals: false, use: popular_now }", "equals: false, use: home }", "privacy.opt_out.use", "seed: none"},
		{"opt-out on an unknown attribute", "attribute: personalization, equals", "attribute: personalisation, equals", "privacy.opt_out.attribute", "not an attribute of user"},

		// feedback
		{"same on an unknown attribute", "applies_to: { same: seller_id }", "applies_to: { same: shop_id }", "feedback.reasons.fewer_from_seller.applies_to.same", "not an attribute"},
		{"two applies_to forms", "applies_to: { same: category }", "applies_to: { same: category, similar: 0.5 }", "feedback.reasons.have_it.applies_to", "item, { same"},
		{"penalty above 1", "effect: { penalty: 0.8 }", "effect: { penalty: 1.5 }", "feedback.reasons.fewer_from_seller.effect.penalty", "(0, 1]"},
		{"positive taste", "effect: { taste: -1 }", "effect: { taste: 1 }", "feedback.reasons.not_my_taste.effect.taste", "negative"},
		{"unknown effect", "effect: satiate", "effect: forget", "feedback.reasons.have_it.effect", "hide, satiate"},
		{"bad until", "until: 30d", "until: soon", "feedback.reasons.seen_too_often.until", "duration, forever or recurrence"},
		{"bad also", "also: report", "also: email", "feedback.reasons.offensive.also", "report"},
		{"reason label without the default locale", "label: { en: This is offensive, ro: Este ofensator, ru: Это оскорбительно }", "label: { ro: Este ofensator, ru: Это оскорбительно }", "feedback.reasons.offensive.label", "default locale"},
		{"unknown similarity term", "terms: [category, tags]", "terms: [category, colour]", "feedback.reasons.not_my_taste.applies_to.terms", "not a similarity term"},

		// recurrence
		{"recurrence of a non-fulfilling interaction", "of: [purchase, owned]\n  group_by", "of: [purchase, view]\n  group_by", "recurrence.of[1]", "fulfils"},
		{"group by an unknown attribute", "group_by: category\n  learn", "group_by: aisle\n  learn", "recurrence.group_by", "not an attribute"},
		{"from a non-numeric attribute", "from: category.recurrence_days", "from: category.parent_id", "recurrence.from", "numeric attribute"},
		{"never_below of 1 or more", "never_below: 0.10", "never_below: 1.5", "recurrence.never_below", "[0, 1)"},
	})
}

func TestV2ShelfRules(t *testing.T) {
	runV2Cases(t, "shelf.yml", []v2Case{
		{"vocab with two types", "      genres:      { type: set, of: string, vocab: genre }", "      genres:      { type: categorical, vocab: genre }", "entities.film.attributes.genres.vocab", "share a type"},
		{"cross attribute without a shared vocab", "      genres:       { type: set, of: string, vocab: genre }", "      genres:       { type: set, of: string }", "similarity.cross[0].on", "same vocab"},
		{"cross between one type", "between: [book, film]", "between: [book, book]", "similarity.cross[0].between", "two different"},
		{"for something nobody recommends", "books_like_yours: { type: item_neighbors, for: [book],", "books_like_yours: { type: item_neighbors, for: [author],", "signals.books_like_yours.for", "not recommended"},
		{"sequence of a non-session interaction", "of: [watch], gap: 6h", "of: [read], gap: 6h", "signals.next_episode.of[0]", "session"},
		{"mutuals via a non-user interaction", "via: follow", "via: watch", "signals.mutuals.via", "target user"},
		{"target on a categorical", "on: pages, target: knob", "on: author, target: knob", "signals.short_read.on", "range"},
		{"unknown target", "on: pages, target: knob", "on: pages, target: median", "signals.short_read.target", "target must be"},
		{"satiation level without a hierarchy", "by: author, default: -0.3", "by: author, level: 1, default: -0.3", "signals.already_read.level", "parent"},
		{"constraint scoped to the wrong type", "- { id: age_ok,       for: [film],", "- { id: age_ok,       for: [book],", "constraints[0].exclude.attribute", "not an attribute of book"},
		{"cross-type weight without from", "signals.films_like_yours.from.book", "signals.books_like_yours.from.film", "knobs[0].maps.signals.books_like_yours.from.film", "does not learn from"},
		{"target of a non-numeric attribute", "attribute.book.pages.target", "attribute.book.author.target", "knobs[2].maps.attribute.book.author.target", "numeric attribute"},
		{"mix shares not summing to 1", "shares: { book: 0.5, film: 0.5 } }\n    knobs:", "shares: { book: 0.5, film: 0.3 } }\n    knobs:", "recommenders.home.mix.shares", "summing to 1"},
		{"fallback to another type", "    constraints: [age_ok]\n\n  watch_party:", "    constraints: [age_ok]\n    fallback:\n      - { when: \"seed.size == 0\", use: people_you_may_know }\n\n  watch_party:", "recommenders.up_next.fallback[0].use", "returns user"},
		{"group without a group seed", "seed: users", "seed: user", "recommenders.watch_party.group", "seed: users"},
		{"mutuals outside people recommendations", "signals: [books_like_yours, films_like_yours, people_like_you, new_releases, short_read, already_read]", "signals: [books_like_yours, films_like_yours, people_like_you, new_releases, short_read, already_read, mutuals]", "recommenders.home.signals", "for: [user], seed: user"},
		{"sequence without an item seed", "signals: [books_like_yours, films_like_yours, people_like_you, new_releases, short_read, already_read]", "signals: [books_like_yours, films_like_yours, people_like_you, new_releases, short_read, already_read, next_episode]", "recommenders.home.signals", "(sequence) needs an item"},
	})
}

func TestV2SpotifyRules(t *testing.T) {
	runV2Cases(t, "spotify.yml", []v2Case{
		{"unknown term", "terms: [audio, embedding]", "terms: [audio, embeding]", "signals.sounds_like.terms", "not a similarity term"},
		{"context match against a user value", "against: $context.title_words", "against: $user.country", "signals.title_match.against", "$context"},
		{"unknown context field in a condition", "contains: \"$context.genre\"", "contains: \"$context.genr\"", "constraints[5].require.contains", "not a declared context field"},
		{"seed target in a user-seeded recommender", "signals: [content, collaborative, familiarity, popularity, trending, recency, exploration, artist_spread]", "signals: [content, collaborative, familiarity, popularity, trending, recency, exploration, artist_spread, energy_fit]", "recommenders.home.signals", "items or session seed"},
		{"seed statistic of a non-numeric attribute", "\"0.1 * min(seed.size, 5) / 5\"", "\"0.1 * seed.mean.genres\"", "recommenders.playlist_add.weights.energy_fit", "not a numeric attribute"},
	})
}

// The v2 sections are additions: a variant may test T1 changes only with a shadow precompute,
// and the legitimate shapes of each section validate.
func TestV2ValidShapes(t *testing.T) {
	base := exampleText(t, "shop.yml")
	for _, c := range []struct{ name, old, new string }{
		{"shadow precompute allows a T1 change", "    on_finish: propose\n", "    on_finish: propose\n    shadow_precompute: true\n"},
		{"single recommendable as a list", "recommendable: listing", "recommendable: [listing]"},
		{"a constraint added by a variant", "          rules.seller_cap.quota.max: 3", "          rules.seller_cap.quota.max: 3\n          constraints.cheap: { require: { attribute: price, lt: \"1000\" } }"},
		{"knob default changed by a variant", "          rules.seller_cap.quota.max: 3", "          rules.seller_cap.quota.max: 3\n          knobs.taste_vs_crowd.default: 0.7"},
		{"plain-string texts", "label: { en: Taste vs crowd, ro: Gust sau mulțime, ru: Вкус или большинство }", "label: Taste vs crowd"},
	} {
		t.Run(c.name, func(t *testing.T) {
			text := strings.Replace(base, c.old, c.new, 1)
			if c.name == "shadow precompute allows a T1 change" {
				text = strings.Replace(text, "          rules.seller_cap.quota.max: 3", "          rules.seller_cap.quota.max: 3\n          signals.trending.window: 3h", 1)
			}
			if issues := issuesFor(t, text); len(issues) != 0 {
				t.Errorf("want no issues, got %v", issues)
			}
		})
	}
}

// Shapes the strict parser must refuse before validation even starts.
func TestV2ParseErrors(t *testing.T) {
	base := exampleText(t, "shop.yml")
	for name, edit := range map[string][2]string{
		"misspelt section":          {"recommenders:\n  home:", "recomenders:\n  home:"},
		"fulfils false":             {"weight: 8.0, half_life: 180d, fulfils: true }", "weight: 8.0, half_life: 180d, fulfils: false }"},
		"applies_to word":           {"applies_to: item\n      effect: hide\n      until: 30d", "applies_to: everything\n      effect: hide\n      until: 30d"},
		"unknown key in applies_to": {"applies_to: { same: category }", "applies_to: { same: category, also: x }"},
		"text as a list":            {"label: { en: For you, ro: Pentru tine, ru: Для вас }", "label: [For you]"},
		"unknown recommender key":   {"    seed: none\n", "    seed: none\n    sead: user\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(base, edit[0]) {
				t.Fatalf("test setup: %q not found", edit[0])
			}
			if _, err := Parse([]byte(strings.Replace(base, edit[0], edit[1], 1))); err == nil {
				t.Error("want a parse error")
			}
		})
	}
}
