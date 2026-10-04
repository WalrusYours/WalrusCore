# Schema examples

This page explains what a schema is made of, lists thirteen complete examples, and follows a
schema from upload to going live. For what a schema is and why WALRUS uses one, see the
[schema overview](../README.md). For the integration steps around it (running WALRUS,
sending events, reading recommendations) see
[`.claude/ADAPT.md`](../../../../.claude/ADAPT.md).

## Anatomy of a schema

A schema has up to eight top-level keys. Only the first five are always needed.

```yaml
version: 1
entities:     { ... }   # what exists
interactions: { ... }   # what users do
similarity:   { ... }   # how things are compared
signals:      { ... }   # how a score is built
knobs:        [ ... ]   # what the end user can move
presets:      { ... }   # named knob positions
constraints:  [ ... ]   # hard rules
recommendable: post     # which entity is ranked
```

The parser rejects unknown keys, so a typo such as `halflife` is an error instead of a
silently ignored rule. Names you invent (entities, attributes, interactions, signals,
knob ids, presets) must match `^[a-z][a-z0-9_]*$`, because they end up in database labels
and property names.

### `version`

An integer of at least 1. It is the version of the schema language, not of your schema;
WALRUS numbers your pushes itself (see the pipeline below).

### `entities`

Declares what exists and which attributes you will send. Anything not declared is rejected
at ingest, so mistakes surface early. An entity named `user` is required, because
constraints and signals refer to `$user`.

```yaml
entities:
  post:
    key: id
    attributes:
      topic:      { type: categorical }
      created_at: { type: timestamp }
```

| Word | Meaning |
|------|---------|
| `key` | The name of the id field, normally `id`. Required. |
| `attributes` | The fields you will send, each with a `type`. |
| `type` | One of `categorical` (a small set of labels such as a topic), `string` (free text), `float`, `int`, `bool`, `timestamp`, `set` (a list of strings such as tags), `vector` (an embedding), `ref` (an id of another entity), `geo` (a point on the Earth, sent as `{ "lat": 47.01, "lon": 28.86 }`). |
| `of` | For `set`: the element type, currently `string`. |
| `entity` | For `ref`: which entity it points to, for example `{ type: ref, entity: user }`. |
| `dim` | For `vector`: the number of dimensions. |
| `range` | For `float` and `int`: `[min, max]`, with min below max. |
| `sensitive` | The attribute counts in scoring and constraints but is never named in a reason. |
| `optional` | The attribute may be missing. Not allowed together with `computed`. |
| `computed` | An expression over the entity's other attributes, evaluated at ingest, for example `"len(title)"`. The platform never sends a computed attribute. Cycles are rejected. |

### `interactions`

Turns events into weighted edges that fade over time. A `like` is a stronger statement than
a `view`; a `dislike` is a negative one.

```yaml
interactions:
  view: { weight: 1.0, value: seconds, transform: log1p, half_life: 3d }
  like: { weight: 4.0, half_life: 30d }
```

| Word | Meaning |
|------|---------|
| `weight` | How strongly the event counts. Negative means the user dislikes the target. |
| `value` | The name of a number your event carries (seconds viewed, fraction listened). Omit it for plain yes/no events. |
| `transform` | Turns that raw number into a bounded contribution: `identity`, `log1p`, `sqrt`, `clamp`, or an expression in `v`, for example `"1 - min(v, 30) / 30"`. Needs `value`. |
| `half_life` | How long until an event counts half as much, for example `3d`. Omit it for no decay. |
| `target` | The entity the event points at, when it is not the recommendable one (for example `follow_artist` targets `artist`). |
| `locked` | Editing guard for the dashboard (see below). |

Durations use `s`, `m`, `h`, `d`, `w`.

### `similarity`

Defines how two items, or two users, are compared. WALRUS precomputes the nearest
neighbours from this, and the neighbour signals read them. It is a map from entity name to
a list of terms; the scores of the terms are combined using their weights.

```yaml
similarity:
  post:
    - { on: topic, metric: equals,  weight: 0.5 }
    - { on: tags,  metric: jaccard, weight: 0.5 }
  user:
    - { via: interactions, metric: cosine }
```

| Word | Meaning |
|------|---------|
| `on` | The attribute (or list of attributes) to compare. |
| `via: interactions` | Compare users by their interaction history instead of by attributes. Use it for `user`. |
| `metric` | `equals` (same value), `jaccard` (overlap of two sets), `cosine` (angle between vectors or number lists), `log_ratio` (closeness of two positive numbers such as prices), `closeness` (one number: 1 minus the gap as a share of the whole range, for a feature like energy or tempo). |
| `weight` | Share of this term in the combined similarity. Must be above 0 for `on` terms. |

A term uses `on` or `via`, never both.

### `signals`

The named parts of a score. The score of an item for a user is the weighted sum of the
signals, using the current weights. The `type` picks a built-in behaviour; the other keys
configure it.

```yaml
signals:
  content: { type: item_neighbors, default: 0.5 }
  recency: { type: age_decay, default: 0.3, on: created_at, half_life: 2d }
```

| Type | Rewards |
|------|---------|
| `item_neighbors` | Items similar to what the user already engaged with. |
| `user_neighbors` | Items that users with a similar history engaged with. |
| `own_history` | Items the user has seen before (familiarity), without a hard exclusion. |
| `global_count` | Popularity over a `window`, for example `window: 7d`. Size, not speed. |
| `trend` | Items whose recent engagement is well above what is ordinary for them: acceleration, not size. Needs `window` and, for `against: own` or `auto`, a longer `baseline`. See below. |
| `age_decay` | Fresh items. Needs `on: <timestamp attribute>` and a `half_life`. |
| `low_exposure` | Items few people have seen, for discovery. |
| `attribute_match` | Items whose attribute `on` matches a user value given in `against: "$user.<attribute>"`. |
| `diversity_rerank` | Spreads results over the values of the attribute `on`, so one author or seller cannot fill the feed. |

`default` is the weight when the user has touched no knob. A new signal type is code
(register it in the signal registry); using an existing type is only configuration.

A `trend` signal says what a trend is for your platform:

```yaml
trending:
  type: trend
  default: 0.2
  window: 6h            # "now": the recent period measured
  baseline: 7d          # the earlier period that defines an item's own ordinary pace
  against: auto         # own | same_age | auto
  on: created_at        # timestamp giving an item's age (not needed for against: own)
  of: [like, comment, view]   # interactions that count (default: every positive-weight one)
  count: people         # people | events
  min: 3                # fewer engagements than this in the window is never a trend
  ratio: 2              # how many times its ordinary pace counts as trending
```

It reads as one sentence: "at least 3 different people engaged in the last 6 hours, at
twice the usual pace". `against: own` compares an item with its own earlier pace (it catches
comebacks), `same_age` with what items of the same age normally get (it judges new releases
fairly), and `auto` uses `own` once an item has enough history and `same_age` before.
Required: `window`; `baseline` unless `against: same_age`; `on` unless `against: own`.
Defaults: `against: auto`, `count: people`, `min: 1`, `ratio: 2`, and every positive-weight
interaction for `of`. Unknown keys are rejected. A trend scores `max(0, ln(pace / ratio))`, where `pace` is (recent + 1) / (ordinary + 1), once the recent engagement reaches `min`.

### `knobs`

The only controls the end user ever sees. Each knob is a projection onto one or more signal
weights, so you decide what can be weighted and the user decides how much.

```yaml
knobs:
  - id: explore
    label: "Safe picks  <->  Surprise me"
    range: [0, 1]
    maps: { exploration: "x", author_spread: "0.3 + 0.5 * x" }
```

| Word | Meaning |
|------|---------|
| `id` | Stable identifier, unique among knobs. |
| `label` | Plain-language text shown to users. Required. Write both ends of the slider. |
| `range` | `[min, max]` of the slider, min below max. |
| `maps` | Where the knob acts. Each key is a signal id or a meta-parameter; each value is an expression in `x`, the knob position. |

Meta-parameters you can map to: `interactions.half_life_scale` (stretches or shrinks every
half-life, which gives a "this week versus all-time" knob) and `constraint.energy_center`
(a soft preference, not a filter).

Expressions use numbers, `x`, `+ - * /`, comparisons, `&& || !`, and the functions `min`,
`max`, `abs`, `round`, `sqrt`, `log1p`, `clamp(x, lo, hi)`, `lerp(a, b, t)` and
`if(cond, a, b)`; `len`, `words`, `lower`, `upper` and `contains` are available in computed
attributes. They are compiled once when the schema loads, so moving a slider never parses
anything. Division by zero gives 0.

### `presets`

Named knob positions the user can pick in one tap. A preset sets **knobs**, not signals,
and every value must lie inside the knob's range.

```yaml
presets:
  default:  { taste_vs_crowd: 0.5, horizon: 0.5, explore: 0.2 }
  discover: { explore: 0.9, taste_vs_crowd: 0.8 }
```

### `constraints`

Hard rules, applied before scoring. They are never weighted, and no knob can override them.
Each constraint is either `require` or `exclude`, with one condition:

```yaml
constraints:
  - require: { attribute: status, equals: active }
  - exclude: { interacted: [dislike] }
  - exclude: { interacted: [skip], count_gte: 3, within: 30d }
  - exclude: { attribute: seller_id, in: "$user.blocked_sellers" }
  - require: { attribute: age, gte: "$user.min_age" }                              # inclusive range
  - require: { attribute: author_id, equals: "$seed.author_id" }                    # same author as the seed
  - require: { attribute: location, within_km: 15, of: "$context.location" }       # near the request
```

| Word | Meaning |
|------|---------|
| `require` / `exclude` | Keep only items that match, or drop the items that match. Use one per constraint. |
| `attribute` | Test an attribute of the recommendable entity. Needs exactly one operator below. |
| `equals`, `in`, `gt`, `gte`, `lt`, `lte`, `contains` | The test (`gte` and `lte` include the bound). The right side may use `$user.<attribute>`, `$context.<field>` or `$seed.<attribute>`. |
| `within_km`, `of` | The attribute is a `geo` point at most this far (great-circle kilometres) from the place `of`: `$context.<geo field>` or `$user.<geo attribute>`. |
| `interacted` | Test the user's history instead: a list of interaction names. |
| `count_gte` | Only with `interacted`: at least this many such events. |
| `within` | Only with `interacted`: only events inside this period, for example `30d`. |
| `when` | Apply the constraint only if this expression is true, for example `"$user.explicit_allowed == false"`. |

`$seed.<attribute>` is the value of an attribute on the seed items: "more from this author", "other
lessons in this course". For an `item` seed it is that item's value; for `items` and `session` seeds it
is the set of the seed items' values, and `equals` or `in` match any of them. Only recommenders with an
`item`, `items` or `session` seed may list such a constraint.

A condition that reads something the request does not have (no seed items found, a `$user` attribute the
user does not have, a context field that was not sent, a place that was not given) does not apply, so
missing data never empties a list.

### `recommendable`

The entity that is ranked. It can be left out when exactly one entity besides `user` is
declared; with more, name it.

### `locked`

Add `locked: true` to an interaction, signal, similarity term or knob to stop it being
edited by accident in the dashboard. It is an editing guard, not access control: the engine
stores the flag but does not reject a push that changes a locked value. The API key scope
decides who may push.

## Examples

Thirteen complete `schema.yml` files, one per kind of platform. Copy the closest one, rename the
entities and attributes to match your data, and push it. Each file is checked by the
server's test suite, so they stay valid as the schema language evolves. The first six use
only the v1 sections; `shop.yml`, `shelf.yml` and the playlist part of `spotify.yml` use the
schema v2 sections (recommenders, context, rules, metrics, experiments, feedback, recurrence).

| Example | Platform | What it shows |
|---------|----------|---------------|
| [`feed.yml`](feed.yml) | Social feed (the demo app) | The smallest useful schema: views, likes, comments, three knobs |
| [`marketplace.yml`](marketplace.yml) | Second-hand marketplace | `$user.` references, a proximity signal, a blocked-sellers constraint, price similarity |
| [`spotify.yml`](spotify.yml) | Music streaming | Audio-feature similarity, negative interactions (skip), a time-window constraint (`within: 2h`); v2: "you might also add" for a playlist (`seed: items`, co-playlisting, the playlist's centre as a target) |
| [`playlist.yml`](playlist.yml) | "You might also add" for a playlist | The smallest schema for a recommender with a seed of items, written step by step: playlist co-listing, similarity terms, a target near the playlist's average energy, a one-per-artist rule, two knobs, and an empty-playlist fallback by title |
| [`shop.yml`](shop.yml) | Marketplace, v2 | After a purchase: fewer substitutes, what goes with it, the same kind again when due; "not interested" with reasons; sponsored slots and seller caps; metrics, an A/B test, a holdout, the non-profiled option |
| [`shelf.yml`](shelf.yml) | Books and films, v2 | Two recommendable types, taste across them (`from`, `similarity.cross`), a mixed list, up next, a watch party, people you may know, interleaving |
| [`news.yml`](news.yml) | News reader | `computed` attributes (reading time) and `locked` values; a knob that widens topics |
| [`jobs.yml`](jobs.yml) | Job board | A computed `skill_count`, `apply` and `hide` interactions that exclude |
| [`courses.yml`](courses.yml) | Online learning | Excluding completed courses and recently dropped ones |
| [`movies.yml`](movies.yml) | Film streaming | Star ratings (explicit, centred on 3), the platform's own model blended in (`provided`), boosting originals and burying old titles, "more from this director" (`$seed`), an age limit from a sensitive attribute, a slider that depends on a switch |
| [`social.yml`](social.yml) | Social network | A follow graph: one feed of only the accounts you follow, one of those you do not, "more from this author", who to follow (people recommended to people) |
| [`local.yml`](local.yml) | Places near you | `geo` points, nearer is better (`proximity`), a radius that is a hard filter (`within_km`), a saved home place, "other branches of this chain", a budget in the request |
| [`dating.yml`](dating.yml) | Dating | Two-sided matching (`reciprocal`, planned in the engine), mutual preferences as filters (gender, an inclusive age range, distance), sensitive attributes that are never explained, "they already liked you" as host-supplied scores, a once-a-day cap |

## What happens when you upload a schema

The schema is pushed to WALRUS; WALRUS never fetches it from you. Every push, from the
dashboard, a script or CI, goes through the same steps.

```
 you write schema.yml
        |
        v
 1. validate offline        (optional, same checks, no server needed)
        |
        v
 2. PUT /v1/schema          (admin key; add ?dry_run=true to only check)
        |
        v
 3. parse                   YAML -> typed schema; unknown keys are an error
        |
        v
 4. validate                every problem is returned at once, with its path
        |
        v
 5. diff against current    verdict: none / additive / breaking
        |
        +-- dry run ------------------> report only, nothing changes
        +-- unchanged ----------------> no new version
        +-- breaking, no confirmation -> 409, nothing changes
        |
        v
 6. record a version        number, hash, author, time, the YAML itself
        |
        v
 7. compile and activate    knob expressions and transforms become functions
        |
        v
 live: new requests use the new schema
```

1. **Validate offline.** The same loader the server uses can check a file with no server and
   no key (`walrusctl schema validate`, planned; see below). Use it in an editor hook or
   a pull request.
2. **Push.** `PUT /v1/schema` with the YAML as the body, authenticated with the admin key.
   Query options: `dry_run=true` runs every check and returns the diff without changing
   anything; `confirm_breaking=true` allows a breaking change.
3. **Parse.** The YAML is decoded into the fixed Go structs that model the schema language.
   Unknown keys fail here, and so does an empty or malformed document. Bodies over the size
   limit are refused with 413.
4. **Validate.** References and types are checked: every `ref` points to a declared
   entity, `similarity.on` names real attributes, signal types and metrics exist, an
   `age_decay` signal points at a timestamp, every knob maps to a declared signal or
   meta-parameter with a valid expression in `x`, presets stay inside knob ranges,
   constraints name real attributes and interactions, computed attributes have no cycles,
   and every `$user.x` names an attribute of `user`. All issues come back together as a
   list of `{path, message}`, with status 400.
5. **Diff.** The new schema is compared with the active one. Adding things, changing
   weights, labels, knobs, presets or similarity is **additive** and applies live. Removing
   an entity, attribute, interaction or signal, or changing an attribute or signal type, is
   **breaking**: stored data or the neighbour graph must be rebuilt. A breaking change
   without `confirm_breaking=true` returns 409 with the diff and changes nothing. A schema
   identical to the active one creates no new version.
6. **Record.** An accepted schema is stored as the next version: its number, a hash of the
   canonical form, the author, the time and the YAML. `GET /v1/schema` returns the active
   one and `GET /v1/schema/history` lists every version, which gives rollback, audit and
   diffs.
7. **Compile and activate.** The schema is turned into an in-memory form: knob maps and
   transforms become functions, attribute names are resolved. The new version replaces the
   old one atomically, so a request sees either the old or the new schema, never a mix.
   The compiled form is never stored; it is rebuilt from the YAML.

A push never needs a code change, a rebuild or a restart. The schema only selects and
configures built-in signal types and similarity metrics.

After a breaking change, re-import the affected entities and events and re-run
precompute so stored data and similarity match the new schema; the 409 response says what is
required.

### Getting the file there

- **CI (recommended).** Keep `schema.yml` in your repository. On pull requests run a
  dry run as a gate; on merge to main push it for real.
- **Boot sync.** Your backend pushes the schema from the SDK at startup. An unchanged
  schema creates no new version, so this is safe on every start.
- **Manual.** `curl` by an admin, for experiments:

```
curl -X PUT -H "Authorization: Bearer $WALRUS_ADMIN_KEY" \
     --data-binary @schema.yml "$WALRUS_URL/v1/schema?dry_run=true"
curl -X PUT -H "Authorization: Bearer $WALRUS_ADMIN_KEY" \
     --data-binary @schema.yml "$WALRUS_URL/v1/schema"
```

### Current status

The push endpoint, parsing, validation, diff, versioning and compilation described above
are implemented and tested, and every example here is pushed by the test suite. Three
things in this description are still target design rather than code: the `walrusctl schema
validate | diff | apply` commands (the binary is a scaffold), per-tenant storage of schema
versions in Neo4j (versions are kept in memory today), and scoped `schema:write` keys (the
push currently requires the admin key). The schema language itself will not change when
they land.

What the engine ranks today is narrower than what the schema language accepts. A push that is
accepted lists, under `warnings`, everything it asks for that the engine does not do yet. That is
today: the `trend`, `own_history`, `satiation`, `recurrence`, `formula`, `attribute_match`,
`attribute_value`, `sequence` and `mutual_connections` signals, diversity re-ranking, `mix`, `group`,
`reciprocal`, and the `place`, `cap` and `pin` rules. `dating.yml` relies on `reciprocal`: until the engine
scores both sides, the list is ranked from the requester's side only.
