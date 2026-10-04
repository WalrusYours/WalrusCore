package store

import (
	"context"
	"errors"

	"github.com/timurcravtov/walrus/internal/domain"
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

	// Version changes whenever anything is written, so a reader can tell whether what it computed
	// from the store is still current.
	Version(ctx context.Context) (uint64, error)
}
