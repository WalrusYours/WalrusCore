package rank

import (
	"math"
	"sort"

	"github.com/timurcravtov/walrus/internal/domain"
)

// User-to-user similarity is separate from item-to-item similarity. The item side compares what
// items are (their attributes) or how they are used together (co-occurrence). This side compares
// people by what they interacted with: two users are alike when they like the same things, so what
// one likes is a suggestion for the other.

const (
	defaultUserK         = 50
	defaultUserMinShared = 1
)

// vector is one user's taste: each liked item with how strongly.
type vector map[int]float64

// userData is what the snapshot keeps for comparing people: the schema's `similarity.user`
// settings and every user's taste vector. None of it depends on weights.
type userData struct {
	k         int
	minShared int
	profiles  map[domain.UserID]vector
}

type neighbour struct {
	user domain.UserID
	sim  float64
	vec  vector
}

type userModel struct {
	neighbours []neighbour
	holders    map[int]int // item -> how many neighbours have it, for the reason shown
	totalSim   float64
}

// userProfiles builds userData once per snapshot. Each interaction counts with its schema weight
// times its transformed value, fading with its half-life.
func (s *snapshot) userProfiles() *userData {
	if s.users != nil {
		return s.users
	}
	d := &userData{k: defaultUserK, minShared: defaultUserMinShared, profiles: map[domain.UserID]vector{}}
	var of []string
	damp := false
	for _, t := range s.sch.Similarity["user"] {
		if t.Via != "interactions" {
			continue
		}
		of, damp = t.Of, t.DampPopular
		if t.K > 0 {
			d.k = t.K
		}
		if t.MinShared > 0 {
			d.minShared = t.MinShared
		}
		break
	}

	for _, it := range s.interactions {
		i, ok := s.index[it.Target]
		rule, known := s.c.Interactions[it.Type]
		if !ok || !known || !s.counts(it.Type, of) {
			continue
		}
		w, err := rule.EdgeWeight(it.Value)
		if err != nil || w <= 0 {
			continue
		}
		if rule.HalfLife > 0 {
			w *= math.Exp2(-float64(s.now.Sub(it.TS)) / float64(rule.HalfLife))
		}
		if d.profiles[it.User] == nil {
			d.profiles[it.User] = vector{}
		}
		d.profiles[it.User][i] += w
	}
	if damp {
		holders := map[int]int{}
		for _, v := range d.profiles {
			for i := range v {
				holders[i]++
			}
		}
		for _, v := range d.profiles {
			for i := range v {
				v[i] /= math.Log(2 + float64(holders[i]))
			}
		}
	}
	s.users = d
	return d
}

// counts reports whether an interaction type feeds user similarity: the types the schema lists, or
// any positive one when it lists none.
func (s *snapshot) counts(interaction string, of []string) bool {
	if len(of) > 0 {
		return contains(of, interaction)
	}
	return s.positive(interaction)
}

// neighboursFor finds the users most like the query: the seed items, plus the requesting user's
// own history scaled by blend. The requester is never their own neighbour.
func (s *snapshot) neighboursFor(blend float64) *userModel {
	d := s.userProfiles()
	q := vector{}
	for _, seedItem := range s.seed {
		q[seedItem] = 1
	}
	if own := d.profiles[s.user]; s.user != "" && blend > 0 && len(own) > 0 {
		top := 0.0
		for _, w := range own {
			top = max(top, w)
		}
		for i, w := range own {
			q[i] += blend * w / top
		}
	}

	var found []neighbour
	if qNorm := norm(q); qNorm > 0 {
		for user, v := range d.profiles {
			if user == s.user {
				continue
			}
			shared, dot := 0, 0.0
			for i, w := range q {
				if wv, ok := v[i]; ok {
					shared++
					dot += w * wv
				}
			}
			if shared >= d.minShared && dot > 0 {
				found = append(found, neighbour{user: user, sim: dot / (qNorm * norm(v)), vec: v})
			}
		}
	}
	sort.SliceStable(found, func(a, b int) bool {
		if found[a].sim != found[b].sim {
			return found[a].sim > found[b].sim
		}
		return found[a].user < found[b].user
	})
	found = found[:min(len(found), d.k)]

	m := &userModel{neighbours: found, holders: map[int]int{}}
	for _, n := range found {
		m.totalSim += n.sim
		for i := range n.vec {
			m.holders[i]++
		}
	}
	return m
}

// likedByNeighbours are the items the most similar users have, strongest first. It uses the blend
// the schema declares, not the knob, so candidates stay independent of weights.
func (s *snapshot) likedByNeighbours(cap int) []int {
	blend := 0.0
	if s.spec.BlendUser != nil {
		blend = *s.spec.BlendUser
	}
	m := s.neighboursFor(blend)
	score := make([]float64, len(s.items))
	for _, n := range m.neighbours {
		for i, w := range n.vec {
			score[i] += n.sim * w
		}
	}
	return topIndices(score, cap, s.inSeed)
}

// blendUser is how much of the requesting user's own taste joins the seed in this request.
func (rk *ranking) blendUser() float64 {
	if v, ok := rk.in.Meta["recommenders."+rk.recommender+".blend_user"]; ok {
		return max(0, v)
	}
	if rk.spec.BlendUser != nil {
		return max(0, *rk.spec.BlendUser)
	}
	return 0
}

// userModel is this request's neighbours, worked out once even when several signals read them.
func (rk *ranking) userModel() *userModel {
	if rk.model == nil {
		rk.model = rk.neighboursFor(rk.blendUser())
	}
	return rk.model
}

func norm(v vector) float64 {
	sum := 0.0
	for _, w := range v {
		sum += w * w
	}
	return math.Sqrt(sum)
}
