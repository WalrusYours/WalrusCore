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
}
