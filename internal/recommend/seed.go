package recommend

import (
	"fmt"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
)

var seedField = map[string]string{
	schema.SeedUser: "user", schema.SeedItem: "item", schema.SeedItems: "items",
	schema.SeedSession: "session", schema.SeedUsers: "users",
}

// validateSeed checks that the request carries the seed field the recommender's seed type needs,
// and none that belong to another type. `user` is allowed beside any seed: it supplies the saved
// knobs and taste.
func validateSeed(name string, spec schema.RecommenderSpec, req Request) (Seed, error) {
	need := spec.EffectiveSeed()
	sent := map[string]bool{
		schema.SeedItem:    req.Item != "",
		schema.SeedItems:   req.Items != nil,
		schema.SeedSession: req.Session != nil,
		schema.SeedUsers:   req.Users != nil,
	}
	for kind, set := range sent {
		if set && kind != need {
			return Seed{}, fail(400, "seed_mismatch", "%s takes %s, not %s (sent: %s)", name, seedText(need), seedField[kind], sentFields(req, sent))
		}
	}

	s := Seed{Kind: need, User: domain.UserID(req.User)}
	switch need {
	case schema.SeedUser:
		if req.User == "" {
			return Seed{}, fail(400, "seed_mismatch", "%s needs user", name)
		}
	case schema.SeedItem:
		if req.Item == "" {
			return Seed{}, fail(400, "seed_mismatch", "%s needs item, not %s", name, sentFields(req, sent))
		}
		s.Item = domain.EntityID(req.Item)
	case schema.SeedItems:
		if req.Items == nil {
			return Seed{}, fail(400, "seed_mismatch", "%s needs items, not %s", name, sentFields(req, sent))
		}
		if len(req.Items) > maxSeedItems {
			return Seed{}, fail(400, "validation_error", "items holds %d songs; at most %d", len(req.Items), maxSeedItems)
		}
		s.Items = toEntities(req.Items)
	case schema.SeedSession:
		if req.Session == nil {
			return Seed{}, fail(400, "seed_mismatch", "%s needs session, not %s", name, sentFields(req, sent))
		}
		if len(req.Session) > maxSeedItems {
			return Seed{}, fail(400, "validation_error", "session holds %d items; at most %d", len(req.Session), maxSeedItems)
		}
		s.Items = toEntities(req.Session)
	case schema.SeedUsers:
		if len(req.Users) == 0 {
			return Seed{}, fail(400, "seed_mismatch", "%s needs users, a non-empty list", name)
		}
		for _, u := range req.Users {
			s.Users = append(s.Users, domain.UserID(u))
		}
	}
	return s, nil
}

// sentFields lists the seed fields a request carries, for error messages.
func sentFields(req Request, sent map[string]bool) string {
	var got []string
	if req.User != "" {
		got = append(got, "user")
	}
	for _, k := range []string{schema.SeedItem, schema.SeedItems, schema.SeedSession, schema.SeedUsers} {
		if sent[k] {
			got = append(got, seedField[k])
		}
	}
	return fmt.Sprint(got)
}

func seedText(kind string) string {
	if kind == schema.SeedNone {
		return "no seed"
	}
	return seedField[kind]
}

func toEntities(ids []string) []domain.EntityID {
	out := make([]domain.EntityID, len(ids))
	for i, id := range ids {
		out[i] = domain.EntityID(id)
	}
	return out
}
