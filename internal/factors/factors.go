// Package factors is the engine's one learned component: implicit-feedback matrix factorisation
// trained by alternating least squares (Hu, Koren and Volinsky, 2008).
//
// Every item gets a short vector from who interacted with it. A user's vector is not stored: it is
// solved from the user's current history against those item vectors, which is "fold-in". The score
// of an item for a user is the dot product of the two, and because the user's vector is a linear
// combination of the vectors of the items in their history, that score splits exactly over those
// items. The package knows nothing about stores, schemas or requests.
package factors

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
)

const (
	DefaultFactors        = 32
	MaxFactors            = 256
	DefaultRegularization = 0.1
	DefaultAlpha          = 10
	DefaultIterations     = 15
	MaxIterations         = 200
)

// ErrNoData is returned when there is nothing to learn from.
var ErrNoData = errors.New("factors: no interactions to learn from")

// Params are the training settings. They are part of the model: fold-in must use the ones the item
// vectors were trained with.
type Params struct {
	Factors        int     // length of every vector
	Regularization float64 // pulls vectors toward zero; above 0
	Alpha          float64 // how much an interaction's strength raises its confidence; above 0
	Iterations     int     // ALS sweeps
	Seed           uint64  // makes the random start, and so the result, reproducible
}

func DefaultParams() Params {
	return Params{
		Factors: DefaultFactors, Regularization: DefaultRegularization, Alpha: DefaultAlpha,
		Iterations: DefaultIterations, Seed: 1,
	}
}

func (p Params) Validate() error {
	switch {
	case p.Factors < 1 || p.Factors > MaxFactors:
		return fmt.Errorf("factors must be between 1 and %d", MaxFactors)
	case !(p.Regularization > 0) || math.IsInf(p.Regularization, 0):
		return errors.New("regularization must be a number above 0")
	case !(p.Alpha > 0) || math.IsInf(p.Alpha, 0):
		return errors.New("alpha must be a number above 0")
	case p.Iterations < 1 || p.Iterations > MaxIterations:
		return fmt.Errorf("iterations must be between 1 and %d", MaxIterations)
	}
	return nil
}

// Obs is one user's interaction with one item: positions in the lists given to Train, and how
// strong it is (already weighted and faded by the caller). Strengths of the same pair add up.
type Obs struct {
	User, Item int
	Value      float64
}

// Model is a trained item-side model: one vector per item, and the Gram matrix of all of them,
// which fold-in needs. It never changes after it is built, so it is safe to share.
type Model struct {
	Params
	Version      int      // set by whoever stores it
	Of           []string // the interaction types it learned from; set by the caller, for comparing with the schema
	Items        []domain.EntityID
	Vecs         []float32 // len(Items) * Factors, row per item
	Gram         []float64 // Factors * Factors, VᵀV of Vecs
	TrainedAt    time.Time
	Interactions int     // distinct (user, item) pairs learned from
	Users        int     // users with at least one of them
	Loss         float64 // the objective at the end of the last sweep

	index map[domain.EntityID]int
}

// NewModel builds a model from item vectors, computing the Gram matrix and the id index.
func NewModel(p Params, items []domain.EntityID, vecs []float32) (*Model, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if len(vecs) != len(items)*p.Factors {
		return nil, fmt.Errorf("factors: %d items need %d numbers, got %d", len(items), len(items)*p.Factors, len(vecs))
	}
	m := &Model{Params: p, Items: items, Vecs: vecs}
	wide := make([]float64, len(vecs))
	for i, v := range vecs {
		wide[i] = float64(v)
	}
	m.Gram = gramOf(wide, len(items), p.Factors)
	m.index = make(map[domain.EntityID]int, len(items))
	for i, id := range items {
		m.index[id] = i
	}
	return m, nil
}

// Row is the position of an item in the model.
func (m *Model) Row(id domain.EntityID) (int, bool) {
	i, ok := m.index[id]
	return i, ok
}

func (m *Model) vec(row int) []float32 { return m.Vecs[row*m.Factors : (row+1)*m.Factors] }

// Cosine is how closely two items point the same way, in [-1, 1]; 0 when either has no direction.
func (m *Model) Cosine(a, b int) float64 {
	va, vb := m.vec(a), m.vec(b)
	var dot, na, nb float64
	for k := range va {
		x, y := float64(va[k]), float64(vb[k])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

// Entry is one item of a user's history for fold-in: a model row and how strongly the user reacted.
type Entry struct {
	Row   int
	Value float64
}

// Fit is a user's vector solved from their history against a model.
type Fit struct {
	m       *Model
	entries []Entry   // merged by row, values above 0, in row order
	vec     []float64 // the user's vector; all zeros with no history
	chol    []float64 // Cholesky factor of A; nil with no history
}

// FoldIn solves the vector of a user with this history: the best least-squares fit to the history
// with every item the user did not touch counting as a weak "probably not". Rows outside the model
// and values that are not above 0 are ignored. A history with nothing usable gives the zero vector,
// which scores every item 0.
func (m *Model) FoldIn(history []Entry) (*Fit, error) {
	merged := map[int]float64{}
	for _, e := range history {
		if e.Row >= 0 && e.Row < len(m.Items) && e.Value > 0 && !math.IsInf(e.Value, 0) {
			merged[e.Row] += e.Value
		}
	}
	f := &Fit{m: m, vec: make([]float64, m.Factors)}
	if len(merged) == 0 {
		return f, nil
	}
	f.entries = make([]Entry, 0, len(merged))
	for row, v := range merged {
		f.entries = append(f.entries, Entry{Row: row, Value: v})
	}
	sort.Slice(f.entries, func(i, j int) bool { return f.entries[i].Row < f.entries[j].Row })

	n := m.Factors
	a := make([]float64, n*n)
	copy(a, m.Gram)
	for d := 0; d < n; d++ {
		a[d*n+d] += m.Regularization
	}
	rhs := make([]float64, n)
	row := make([]float64, n)
	for _, e := range f.entries {
		w := m.Alpha * e.Value // confidence minus the 1 that Gram already counts
		for k, x := range m.vec(e.Row) {
			row[k] = float64(x)
		}
		addOuter(a, row, w, n)
		for k := range rhs {
			rhs[k] += (1 + w) * row[k]
		}
	}
	if !cholesky(a, n) {
		return nil, errors.New("factors: the history could not be solved; raise regularization")
	}
	copy(f.vec, rhs)
	cholSolve(a, n, f.vec)
	f.chol = a
	return f, nil
}

// Empty reports whether the history gave nothing to learn a vector from.
func (f *Fit) Empty() bool { return f.chol == nil }

// Score is how well an item fits the user: the dot product of their vectors.
func (f *Fit) Score(row int) float64 {
	if f.chol == nil {
		return 0
	}
	var s float64
	for k, x := range f.m.vec(row) {
		s += float64(x) * f.vec[k]
	}
	return s
}

// Top lists the rows with the highest positive scores, best first, at most n, skipping those for
// which skip returns true. Ties keep row order, so the result is reproducible.
func (f *Fit) Top(n int, skip func(row int) bool) []int {
	if f.chol == nil || n <= 0 {
		return nil
	}
	type scored struct {
		row   int
		score float64
	}
	var all []scored
	for row := range f.m.Items {
		if skip != nil && skip(row) {
			continue
		}
		if s := f.Score(row); s > 0 {
			all = append(all, scored{row, s})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	out := make([]int, 0, min(n, len(all)))
	for _, s := range all[:min(n, len(all))] {
		out = append(out, s.row)
	}
	return out
}

// Contribution is how much one history item adds to an item's score.
type Contribution struct {
	Row   int
	Value float64
}

// Contributions splits Score(row) over the history items, largest first. The user's vector is
// A⁻¹ Σ c_j b_j, so the score is Σ_j c_j · (b_rowᵀ A⁻¹ b_j): one term per history item, which sum to
// the score exactly. Negative terms mean that history item counts against the candidate.
func (f *Fit) Contributions(row int) []Contribution {
	if f.chol == nil {
		return nil
	}
	n := f.m.Factors
	w := make([]float64, n)
	for k, x := range f.m.vec(row) {
		w[k] = float64(x)
	}
	cholSolve(f.chol, n, w) // A⁻¹ b_row
	out := make([]Contribution, len(f.entries))
	for i, e := range f.entries {
		var dot float64
		for k, x := range f.m.vec(e.Row) {
			dot += float64(x) * w[k]
		}
		out[i] = Contribution{Row: e.Row, Value: (1 + f.m.Alpha*e.Value) * dot}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	return out
}

// Train learns item vectors from observations: items is the list of every item, users how many
// users there are, obs the interactions. The same inputs and seed always give the same model.
func Train(items []domain.EntityID, users int, obs []Obs, p Params, now time.Time) (*Model, error) {
	return train(items, users, obs, p, now, nil)
}

func train(items []domain.EntityID, users int, obs []Obs, p Params, now time.Time, onSweep func(sweep int, loss float64)) (*Model, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	merged, err := mergeObs(obs, users, len(items))
	if err != nil {
		return nil, err
	}
	if len(merged) == 0 {
		return nil, ErrNoData
	}
	f := p.Factors
	byUser, byItem := newCSR(users, merged, true), newCSR(len(items), merged, false)

	rng := rand.New(rand.NewPCG(p.Seed, p.Seed^0x9e3779b97f4a7c15))
	item := make([]float64, len(items)*f)
	for i := range item {
		item[i] = rng.NormFloat64() * 0.01
	}
	user := make([]float64, users*f)

	var loss float64
	for sweep := 1; sweep <= p.Iterations; sweep++ {
		if err := solveSide(user, byUser, item, gramOf(item, len(items), f), p); err != nil {
			return nil, err
		}
		if err := solveSide(item, byItem, user, gramOf(user, users, f), p); err != nil {
			return nil, err
		}
		if onSweep != nil || sweep == p.Iterations {
			loss = objective(merged, user, item, p)
			if onSweep != nil {
				onSweep(sweep, loss)
			}
		}
	}

	vecs := make([]float32, len(item))
	for i, x := range item {
		vecs[i] = float32(x)
	}
	m, err := NewModel(p, items, vecs)
	if err != nil {
		return nil, err
	}
	m.TrainedAt, m.Loss, m.Interactions = now.UTC(), loss, len(merged)
	for u := 0; u < users; u++ {
		if byUser.start[u+1] > byUser.start[u] {
			m.Users++
		}
	}
	return m, nil
}

// mergeObs adds up repeated pairs, drops values that are not above 0, and sorts by user then item.
func mergeObs(obs []Obs, users, items int) ([]Obs, error) {
	kept := make([]Obs, 0, len(obs))
	for _, o := range obs {
		if o.User < 0 || o.User >= users || o.Item < 0 || o.Item >= items {
			return nil, fmt.Errorf("factors: observation (%d, %d) is outside %d users and %d items", o.User, o.Item, users, items)
		}
		if o.Value > 0 && !math.IsInf(o.Value, 0) {
			kept = append(kept, o)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].User != kept[j].User {
			return kept[i].User < kept[j].User
		}
		return kept[i].Item < kept[j].Item
	})
	out := kept[:0]
	for _, o := range kept {
		if n := len(out); n > 0 && out[n-1].User == o.User && out[n-1].Item == o.Item {
			out[n-1].Value += o.Value
			continue
		}
		out = append(out, o)
	}
	return out, nil
}

// csr is a sparse matrix by rows: row r holds col[start[r]:start[r+1]] with the matching values.
type csr struct {
	start []int
	col   []int32
	val   []float64
}

// newCSR groups observations by user or by item. obs must be sorted by user then item, which keeps
// every row's order, and so every sum's order, fixed.
func newCSR(rows int, obs []Obs, byUser bool) csr {
	c := csr{start: make([]int, rows+1), col: make([]int32, len(obs)), val: make([]float64, len(obs))}
	row := func(o Obs) int {
		if byUser {
			return o.User
		}
		return o.Item
	}
	for _, o := range obs {
		c.start[row(o)+1]++
	}
	for r := 0; r < rows; r++ {
		c.start[r+1] += c.start[r]
	}
	next := make([]int, rows)
	copy(next, c.start[:rows])
	for _, o := range obs {
		r := row(o)
		other := o.Item
		if !byUser {
			other = o.User
		}
		c.col[next[r]], c.val[next[r]] = int32(other), o.Value
		next[r]++
	}
	return c
}

// solveSide is one ALS half-step: with the vectors of the other side fixed, every row's vector
// is the solution of A x = v, where A = G + Σ_obs (c-1) b bᵀ + λI and v = Σ_obs c b. G, the Gram
// matrix of the other side, stands for every pair at once, so the cost follows the number of
// observations, not the number of pairs. Rows are independent, so they run in parallel; each
// writes only its own slot, which keeps the result the same however they are scheduled.
func solveSide(out []float64, adj csr, other, gram []float64, p Params) error {
	n, rows := p.Factors, len(adj.start)-1
	workers := min(runtime.GOMAXPROCS(0), max(rows, 1))
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := rows*w/workers, rows*(w+1)/workers
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, rhs := make([]float64, n*n), make([]float64, n)
			for r := lo; r < hi; r++ {
				from, to := adj.start[r], adj.start[r+1]
				if from == to {
					continue // nothing observed: the best vector is zero
				}
				copy(a, gram)
				for d := 0; d < n; d++ {
					a[d*n+d] += p.Regularization
				}
				clear(rhs)
				for k := from; k < to; k++ {
					b := other[int(adj.col[k])*n : (int(adj.col[k])+1)*n]
					wgt := p.Alpha * adj.val[k]
					addOuter(a, b, wgt, n)
					for q := range rhs {
						rhs[q] += (1 + wgt) * b[q]
					}
				}
				if !cholesky(a, n) {
					errs[w] = errors.New("factors: a vector could not be solved; raise regularization")
					return
				}
				cholSolve(a, n, rhs)
				copy(out[r*n:(r+1)*n], rhs)
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// objective is what ALS minimises: Σ over every user-item pair of c (π - a·b)², plus the penalty.
// Unobserved pairs have c = 1 and π = 0, so together they are Σ_u aᵀ G a; each observed pair
// replaces its p² there with c (1 - p)².
func objective(obs []Obs, user, item []float64, p Params) float64 {
	n := p.Factors
	gram := gramOf(item, len(item)/n, n)
	var loss float64
	for u := 0; u < len(user)/n; u++ {
		a := user[u*n : (u+1)*n]
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				loss += a[i] * gram[i*n+j] * a[j]
			}
		}
	}
	for _, o := range obs {
		var dot float64
		for k := 0; k < n; k++ {
			dot += user[o.User*n+k] * item[o.Item*n+k]
		}
		c := 1 + p.Alpha*o.Value
		loss += c*(1-dot)*(1-dot) - dot*dot
	}
	var norm float64
	for _, x := range user {
		norm += x * x
	}
	for _, x := range item {
		norm += x * x
	}
	return loss + p.Regularization*norm
}

// gramOf is VᵀV for rows vectors of length n, as a full symmetric matrix.
func gramOf(v []float64, rows, n int) []float64 {
	g := make([]float64, n*n)
	for r := 0; r < rows; r++ {
		addOuter(g, v[r*n:(r+1)*n], 1, n)
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			g[i*n+j] = g[j*n+i]
		}
	}
	return g
}

// addOuter adds w · b bᵀ to the lower triangle of the n×n matrix a.
func addOuter(a, b []float64, w float64, n int) {
	for p := 0; p < n; p++ {
		wb := w * b[p]
		if wb == 0 {
			continue
		}
		row := a[p*n : p*n+p+1]
		for q := range row {
			row[q] += wb * b[q]
		}
	}
}

// cholesky factors the symmetric positive definite matrix in the lower triangle of a, in place, as
// L Lᵀ. It reports false when the matrix is not positive definite.
func cholesky(a []float64, n int) bool {
	for j := 0; j < n; j++ {
		d := a[j*n+j]
		for k := 0; k < j; k++ {
			d -= a[j*n+k] * a[j*n+k]
		}
		if !(d > 0) {
			return false
		}
		d = math.Sqrt(d)
		a[j*n+j] = d
		for i := j + 1; i < n; i++ {
			s := a[i*n+j]
			for k := 0; k < j; k++ {
				s -= a[i*n+k] * a[j*n+k]
			}
			a[i*n+j] = s / d
		}
	}
	return true
}

// cholSolve solves L Lᵀ x = b in place on b, given the factor from cholesky.
func cholSolve(l []float64, n int, b []float64) {
	for i := 0; i < n; i++ {
		s := b[i]
		for k := 0; k < i; k++ {
			s -= l[i*n+k] * b[k]
		}
		b[i] = s / l[i*n+i]
	}
	for i := n - 1; i >= 0; i-- {
		s := b[i]
		for k := i + 1; k < n; k++ {
			s -= l[k*n+i] * b[k]
		}
		b[i] = s / l[i*n+i]
	}
}
