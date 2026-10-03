## W.A.L.R.U.S

> Weight And Liberty-based Recommendation Utility System

<div align="center">
    <img src="docs/walrus.png" width=150>
</div>

WALRUS is a recommendation engine you adapt to your platform with a YAML schema. You
describe your data and how a good result is scored; end users get a few knobs to re-weight
the ranking, and every result comes with the reasons it was shown.

## Schema

The schema is one YAML file where a platform declares its entities, the events that matter,
the signals a ranking is built from, the knobs users may move and the rules that always
apply. Adopting WALRUS is a push of that file, with no code change and no database changes.

- [What a schema is and why](docs/schema/README.md)
- [Examples, every key explained and the upload pipeline](docs/schema/schema-examples/README.md)
