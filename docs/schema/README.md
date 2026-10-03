# Schema

## What it is

The schema is a single YAML file, `schema.yml`, that tells WALRUS about your platform. In
it you declare:

- the **entities** you have (users, posts, listings, tracks) and their attributes;
- the **interactions** users have with them (views, likes, purchases) and how much each
  one counts;
- how items are **similar** to each other;
- the **signals** a ranking is built from, such as taste, popularity or freshness;
- the **knobs** end users may move, and the **presets** that set them;
- the **constraints** that must always hold, such as "never show removed items".

It is written once by the platform's developers, kept in the platform's own repository, and
pushed to WALRUS. WALRUS never reads your database and never fetches the file itself.

## Why it exists

**One engine, any content.** A shop, a music service and a news site have different data
and different ideas of a good result. Without a schema the engine would either assume one
shape or need custom code for each platform. With it, the same engine serves all of them,
and adopting WALRUS is a push of a file, not a code change, a rebuild or a database
migration.

**A contract.** The schema is the agreement between the platform and WALRUS. WALRUS checks
it before accepting it and reports every problem at once, so mistakes are found when
the schema is pushed, not when users see bad results. Unknown keys and undeclared
attributes are rejected, which catches typos.

**Control with limits.** The author decides what can be weighted (the signals and their
defaults); the end user decides how much, through the few knobs the author chose. The user
can never go outside the schema, and constraints always apply before any scoring.

**Explanations for free.** Every score is a sum of declared signals, so the same breakdown
that produced a ranking can explain it, in plain language, to the person looking at it.

**Safe change over time.** Every accepted schema is stored as a version. WALRUS tells you
whether a change is additive (applies live) or breaking (stored data must be rebuilt) before
anything changes, and a dry run lets CI check a schema without applying it.

## Where to go next

- [`schema-examples/`](schema-examples/) holds six complete schemas, and
  [`schema-examples/README.md`](schema-examples/README.md) explains every key and
  follows a schema from upload to going live.
