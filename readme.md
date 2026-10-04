<div align="center">

<img src="docs/walrus.png" width=150>
    
# W.A.L.R.U.S

***W*eight *A*nd *L*iberty-based *R*ecommendation *U*tility *S*ystem**

[Website](https://walrusyours.github.io) • [Docs](https://walrusyours.github.io/docs) • [Instalation Guide](https://walrusyours.github.io/install)

[![CI](https://github.com/WalrusYours/WalrusCore/actions/workflows/ci.yml/badge.svg)](https://github.com/WalrusYours/WalrusCore/actions/workflows/ci.yml)

</div>



WALRUS is a recommendation engine you adapt to your platform with a YAML schema. You
describe your data and how a good result is scored; end users get a few knobs to re-weight
the ranking, and every result comes with the reasons it was shown.

## Contents

- [Schema](#schema)
- [API](#api)
  - [Entities](#entities)
  - [Recommend](#recommend)
  - [Explain](#explain)
  - [Errors](#errors)
- [License](#license)
- [Contributing](#contributing)

## Schema

The schema is one YAML file where a platform declares its entities, the events that matter,
the signals a ranking is built from, the knobs users may move and the rules that always
apply. Adopting WALRUS is a push of that file, with no code change and no database changes.

- [What a schema is and why](docs/schema/README.md)
- [Examples, every key explained and the upload pipeline](docs/schema/schema-examples/README.md)

## API

Everything is under `/v1` and JSON. Errors have one shape: `{"error": {"code": "...", "message": "..."}}`.
Send `Authorization: Bearer <admin key>` (the key the engine was started with in `WALRUS_ADMIN_KEY`);
only the health endpoints are open. Scoped keys and per-tenant keys are not built yet.

| Method and path | What it does |
|---|---|
| `GET /health`, `GET /v1/health` | Liveness: `status`, `version`, `instance_id`, `instance_name`. No key. |
| `PUT /v1/schema` | Push the YAML schema as the request body. `?dry_run=true` validates and returns the diff without applying; `?confirm_breaking=true` applies a breaking change. 200 applied, 400 with the paths of every problem, 409 when the change is breaking and needs confirming. An accepted schema can come back with `warnings`: what it asks for that the engine does not do yet. |
| `GET /v1/schema` | The active schema: `yaml`, `version`, `hash`. 404 before the first push. |
| `GET /v1/schema/history` | Every pushed version. |
| `POST /v1/entities` | Send up to 1000 entities (items, users...) as JSON. Each is checked against the schema; bad ones are listed in `rejected` and the rest are stored. |
| `PUT /v1/entities/{type}/{id}` | Create or replace one entity: `{"attributes": {...}}`. 201 when new, 200 when replaced, 400 with the reason when it breaks the schema. |
| `POST /v1/import` | Load a whole catalogue as JSON Lines, one `{"entity", "id", "attributes"}` per line. Reports the first 100 bad lines by number. |
| `POST /v1/interactions` | Send up to 1000 events (`{"user", "type", "target", "ts", "fields"}`), checked against the schema's `interactions`. `GET /v1/interactions` counts them by type. |
| `GET /v1/entities`, `GET /v1/entities/{type}/{id}` | How many entities of each type are stored, and one entity back. |
| `POST /v1/recommenders/{recommender}/recommend` | Recommend with a recommender the schema declares. |
| `GET /v1/recommend/{user}` | Shorthand for a schema with no `recommenders`: the implicit `default` recommender for a user. Takes `?limit=`, `?preset=` and `?knobs.<id>=<number>`. |
| `POST /v1/recommend/{user}` | The same, with a JSON body (for example `exclude`). |
| `GET /v1/recommendations/{rec_id}/explain/{item}` | Why one item is where it is in one earlier list. |
| `POST /v1/admin/session`, `GET /v1/admin/session`, `DELETE /v1/admin/session` | Sign in with `{"key": "..."}` and get a short-lived HttpOnly cookie, check it, sign out. Used by the dashboard so the key is never kept in a browser. |

### Entities

Entities are checked against the pushed schema: the type and every attribute must be declared, each
value must have the declared type (and range), and every attribute that is not `optional` must be
present. Sending the same `type` and `id` again replaces the entity, so loads can be repeated.

```http
POST /v1/entities

{ "entities": [
  { "entity": "track", "id": "back_in_black", "attributes": { "energy": 0.92, "genres": ["rock"], "...": "..." } }
] }
```

```json
{ "accepted": 1, "rejected": [ { "index": 3, "entity": "track", "id": "x", "error": "attribute \"energy\": 1.5 is outside the range [0, 1]" } ] }
```

### Recommend

The schema decides what a recommender takes. `seed:` is one of `user`, `item`, `items`, `session`,
`users` or `none`, and the request carries exactly that field.

```http
POST /v1/recommenders/playlist_add/recommend
Authorization: Bearer <admin key>

{
  "items": ["back_in_black", "thunderstruck", "enter_sandman"],
  "limit": 5,
  "knobs": { "vibe_vs_branch_out": 1 },
  "explain": true
}
```

| Field | Meaning |
|---|---|
| `user`, `item`, `items`, `session`, `users` | The seed. Only the one the recommender's `seed:` names is accepted, otherwise 400 `seed_mismatch`. |
| `context` | Values for the schema's `context` block (device, time of day...). Unknown fields are rejected; `hour`, `weekday` and `month` are filled in by the engine. |
| `limit` | Capped by the recommender's `limit.max`; defaults to `limit.default` (30 and 200 when the schema says nothing). |
| `exclude` | Item ids to leave out. |
| `knobs` | The end user's Tune values by knob id. Only knobs the recommender offers; others are 400. |
| `preset` | A named preset from the schema. |
| `explain` | `true` adds a one-line reason to each item. |
| `scores` | Per-item scores the host supplies for signals of type `provided`. |

```json
{
  "recommender": "playlist_add",
  "used": "playlist_add",
  "rec_id": "rec_1a7751f9b1bde656",
  "items": [
    { "id": "paranoid", "type": "track", "score": 0.678, "reason": "Often added to playlists with Back in Black · Shares rock and hard rock with Thunderstruck" }
  ],
  "weights": { "co_listed": 0.4, "sounds_like": 0.0, "exploration": 0.3 },
  "knobs": { "vibe_vs_branch_out": 1 },
  "experiment": { "id": "trend_weight", "variant": "control" },
  "candidates_considered": 13,
  "took_ms": 1.2
}
```

- `used` differs from `recommender` when a fallback answered (for example an empty playlist
  falling back to one that reads the title).
- `weights` are the final numbers after the schema's defaults, expressions, the experiment
  variant and the user's knobs, so the host can show exactly what was applied.
- `experiment` is present when the user is in a running experiment; `holdout: true` when they
  are in the holdout.
- `rec_id` names this exact list for one minute, which is how it can be explained.

### Explain

`GET /v1/recommendations/{rec_id}/explain/{item}` answers from the stored list, not a recomputation,
so the breakdown is the one that produced the score and always adds up to it. Each signal that
contributed says in words what it found, using the actual songs, genres and numbers involved. The one-line
`reason` in a recommend response joins the two strongest, the second only when it carries real weight.

```json
{
  "rec_id": "rec_1a7751f9b1bde656", "item": "paranoid", "position": 1, "score": 0.678,
  "breakdown": [
    { "signal": "co_listed", "value": 0.267, "because": "Often added to playlists with Back in Black" },
    { "signal": "sounds_like", "value": 0.218, "because": "Close to Thunderstruck in energy and valence" },
    { "signal": "genre_fit", "value": 0.050, "because": "Shares rock and hard rock with Back in Black" }
  ]
}
```

### Errors

| Status | `code` | When |
|---|---|---|
| 400 | `validation_error` | Bad JSON, unknown body field, unknown knob or preset, a knob the recommender doesn't offer, a bad context value. |
| 400 | `seed_mismatch` | The wrong seed field for the recommender. |
| 400 | `unknown_context_field` | A `context` field the schema doesn't declare. |
| 401 | `unauthorized` | Missing or wrong key. |
| 404 | `unknown_recommender` | The schema has no such recommender; a schema that declares its own recommenders has no `default`. |
| 404 | `unknown_recommendation`, `unknown_item` | The `rec_id` expired, or the item was not in that list. |
| 409 | `schema_missing` | Nothing has been pushed yet. |

> **Status.** Entities and interactions are stored in memory, so they are lost on restart. Recommend ranks
> from that store.
>
> The ranker supports these so far. Anything else a schema asks for is skipped, never fatal, and the schema push
> lists it under `warnings`.
>
> - **Signals:** `item_neighbors`, `co_occurrence`, `attribute_target`, `global_count`, `low_exposure`,
>   `age_decay`, `context_match`, `provided` and `user_neighbors`.
> - **Candidate sources:** `item_neighbors`, `co_occurrence`, `user_neighbors` (for user seeds) and `popular`; with
>   none declared, every item of the type.
> - **Constraints:** every form (`in_seed`, attribute `contains`/`equals`/`in`/`gt`/`lt`, `interacted`). A
>   constraint that reads something the request does not have, such as a `$user` attribute, does not apply.
> - **Rules:** attribute quotas (`max` per `per` positions).
> - **Similarity metrics:** `jaccard`, `cosine`, `equals`, `log_ratio` and `closeness`. A knob bound to
>   `similarity.<type>.<term>.weight` sets that term's weight for the recommenders that offer it, which is how
>   each audio feature (energy, mood, danceability, acoustic feel, tempo) gets its own slider.
> - **Not yet:** `trend`, `own_history`, diversity re-ranking and the other rule kinds.
>
> Two kinds of similarity are kept apart. **Item to item** compares what items are (the schema's
> `similarity.<type>` terms) or how playlists and baskets hold them together (`co_occurrence`). **User to
> user** (`similarity.user`) compares people by what they interacted with: the seed items, plus the
> requester's own history scaled by `blend_user`, are matched with every other user by cosine, and the
> closest users' items are suggested. The requester is never their own neighbour.
>
> **Instant re-rank.** Everything that does not depend on weights (the candidates, the hard constraints, the
> similarities between candidates and seed items, co-listing counts and taste profiles) is computed once and kept
> as a snapshot. Moving a Tune slider changes only weights, so the next request reuses the snapshot and only
> re-scores. Any write to the store starts a new snapshot, so nothing is ranked from stale data. The snapshot is
> built from the whole catalogue in memory, which suits catalogues of a few thousand items.
> A reason such as "Similar to Thunderstruck" names songs by their `title` attribute when the entity has one.

## License

WALRUS is licensed under the [Apache License 2.0](LICENSE).

- **You may** use it for anything, including commercially: run it, self-host it, modify it,
  sell a product or a hosted service built on it, and keep your own changes private.
- **You must credit WALRUS.** If you copy or distribute any part of the code, in source or
  binary form and in any project, keep the [LICENSE](LICENSE) and the attribution in
  [NOTICE](NOTICE) (the name WALRUS and its author) with it. In a derivative work, the
  `NOTICE` text must appear in a `NOTICE` file, in the documentation, or in a screen the
  product displays, such as an "About" page. Mark the files you changed.
- The licence includes a patent grant from the author, and the software comes with no
  warranty.

## Contributing

Pull requests are not accepted for now, but bug reports and feature requests are welcome as
issues. See [CONTRIBUTING.md](CONTRIBUTING.md).
