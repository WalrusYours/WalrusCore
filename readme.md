## W.A.L.R.U.S

> Weight And Liberty-based Recommendation Utility System

<div align="center">
    <img src="docs/walrus.png" width=150>
</div>

WALRUS is a recommendation engine you adapt to your platform with a YAML schema. You
describe your data and how a good result is scored; end users get a few knobs to re-weight
the ranking, and every result comes with the reasons it was shown.

## Why a schema

Recommenders usually assume one data shape (users, items, clicks) and one fixed way of
ranking. A shop, a music service and a news site do not fit the same shape, and none of them
want to rewrite their database or fork the engine to fit it.

The schema removes that cost. It is one declarative file in which the platform says:

- **what exists**: its entities and their attributes, in its own words (`listing`,
  `track`, `post`);
- **what users do**: which events count, how much, and for how long;
- **how things relate**: which attributes make two items similar;
- **how to score**: the named signals that make up a ranking;
- **what the user may move**: a few knobs, written in plain language;
- **what is never allowed**: hard constraints that no knob can override.

Because the schema is data, adopting WALRUS is a push, not a code change: the engine is not
rebuilt and your database is not touched. It also makes the two-layer control possible. The
platform author decides *what can be weighted*; the end user decides *how much*, and cannot
step outside what the author declared. And because every score is built from the declared
signals, the same breakdown that produced a ranking can explain it.

## How to define a schema

A schema is a single `schema.yml`. A small but complete one, for a social feed:

```yaml
version: 1

entities:                      # 1. what exists
  user:
    key: id
    attributes:
      interests: { type: set, of: string }
  post:
    key: id
    attributes:
      topic:      { type: categorical }
      created_at: { type: timestamp }
      status:     { type: categorical }

interactions:                  # 2. what users do
  view: { weight: 1.0, value: seconds, transform: log1p, half_life: 3d }
  like: { weight: 4.0, half_life: 30d }

similarity:                    # 3. how things relate
  post:
    - { on: topic, metric: equals, weight: 1.0 }
  user:
    - { via: interactions, metric: cosine }

signals:                       # 4. how a score is built
  content:       { type: item_neighbors, default: 0.5 }
  collaborative: { type: user_neighbors, default: 0.5 }
  recency:       { type: age_decay, default: 0.3, on: created_at, half_life: 2d }

knobs:                         # 5. what the end user can move
  - id: taste_vs_crowd
    label: "My taste  <->  What similar people like"
    range: [0, 1]
    maps: { content: "1 - x", collaborative: "x" }

constraints:                   # 6. what is never allowed
  - require: { attribute: status, equals: active }

recommendable: post            # which entity is ranked
```

To write your own:

1. **Start from the closest example** in [`docs/schema/schema-examples`](docs/schema/schema-examples/)
   (feed, marketplace, music, news, jobs, courses) and rename things to match your data.
2. **Declare entities** for everything you will send, including a `user`. Anything you do
   not declare is rejected at ingest.
3. **List the events you already produce** as interactions, with a weight (negative for
   dislikes) and a half-life.
4. **Pick signals** from the built-in types and set their default weights.
5. **Add two to four knobs.** Each one maps onto signal weights, and its label should name
   both ends of the slider in plain words.
6. **Add constraints** for anything that must never be shown.
7. **Push it.** Use a dry run first; WALRUS returns every problem at once and says whether
   the change is additive or breaking.

What a schema is and why it exists is in [`docs/schema/README.md`](docs/schema/README.md). The full
reference, with every key explained and the step-by-step upload pipeline, is in
[`docs/schema/schema-examples/README.md`](docs/schema/schema-examples/README.md). The integration guide
that wraps the schema (running WALRUS, sending events, serving recommendations) is
`.claude/ADAPT.md` in the thesis workspace.


