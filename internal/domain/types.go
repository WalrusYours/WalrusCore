package domain

import "time"

type (
	EntityID string
	UserID   string
)

// Entity is the single shape for every item; allowed attributes come from the tenant schema.
type Entity struct {
	Type  string
	ID    EntityID
	Attrs map[string]Value
}

type Interaction struct {
	User   UserID
	Type   string
	Target EntityID
	Value  *float64
	TS     time.Time
	Fields map[string]string
}

type Edge struct {
	Target EntityID
	Type   string
	Weight float64
	TS     time.Time
}

type Candidate struct {
	Item    EntityID
	Sources []string
}

type WeightVector map[string]float64

type KnobValues map[string]float64

type Profile struct {
	Knobs     KnobValues
	Preset    string
	UpdatedAt time.Time
}

type ScoredItem struct {
	Item    EntityID
	Score   float64
	Signals map[string]float64
}
