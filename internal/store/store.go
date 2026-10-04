package store

import (
	"context"
	"errors"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/factors"
)

var ErrNotFound = errors.New("store: not found")

type Store interface {
	UpsertEntities(ctx context.Context, entities []domain.Entity) error
	Entity(ctx context.Context, typ string, id domain.EntityID) (domain.Entity, error)
	Entities(ctx context.Context, typ string, ids []domain.EntityID) ([]domain.Entity, error)
	ListEntities(ctx context.Context, typ string) ([]domain.Entity, error)
	CountEntities(ctx context.Context) (map[string]int, error)

	UpsertInteractions(ctx context.Context, interactions []domain.Interaction) error
	Interactions(ctx context.Context, types ...string) ([]domain.Interaction, error)
	// UserInteractions returns one user's interactions in the order they arrived.
	UserInteractions(ctx context.Context, user domain.UserID) ([]domain.Interaction, error)
	CountInteractions(ctx context.Context) (map[string]int, error)

	// SaveModel makes m the active trained model called name (the id of an embedding signal) and
	// returns the version it was given: 1 for the first model under that name, then one more each
	// time. The model the caller passed is not changed. A request in flight keeps the model it
	// already read; readers never see a mix of two.
	SaveModel(ctx context.Context, name string, m *factors.Model) (version int, err error)
	// Model returns the active model called name, or ErrNotFound when none has been trained.
	Model(ctx context.Context, name string) (*factors.Model, error)

	// Version changes whenever anything is written, a model included, so a reader can tell
	// whether what it computed from the store is still current.
	Version(ctx context.Context) (uint64, error)
}
