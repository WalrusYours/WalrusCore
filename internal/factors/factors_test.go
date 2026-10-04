package factors

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
)

var epoch = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func ids(n int) []domain.EntityID {
	out := make([]domain.EntityID, n)
	for i := range out {
		out[i] = domain.EntityID(fmt.Sprintf("i%02d", i))
	}
	return out
}

// twoTastes is a catalogue of 20 items and 40 users in two groups: the first group likes items
// 0-9, the second likes items 10-19, each user about six of their group's ten.
func twoTastes() (items []domain.EntityID, users int, obs []Obs) {
	rng := rand.New(rand.NewPCG(7, 11))
	for u := 0; u < 40; u++ {
		base := 0
		if u >= 20 {
			base = 10
		}
		for _, k := range rng.Perm(10)[:6] {
			obs = append(obs, Obs{User: u, Item: base + k, Value: 1})
		}
	}
	return ids(20), 40, obs
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestCholeskySolve(t *testing.T) {
	a := []float64{4, 0, 2, 3} // lower triangle of [[4, 2], [2, 3]]
	if !cholesky(a, 2) {
		t.Fatal("a positive definite matrix did not factor")
	}
	b := []float64{2, 1}
	cholSolve(a, 2, b)
	if !near(b[0], 0.5, 1e-12) || !near(b[1], 0, 1e-12) {
		t.Errorf("solution = %v, want [0.5 0]", b)
	}
	if cholesky([]float64{1, 0, 2, 1}, 2) { // [[1, 2], [2, 1]] has a negative eigenvalue
		t.Error("an indefinite matrix factored")
	}
}

func TestParamsValidate(t *testing.T) {
	if err := DefaultParams().Validate(); err != nil {
		t.Fatalf("defaults are invalid: %v", err)
	}
	if DefaultParams().Factors != 32 {
		t.Errorf("default factors = %d, want 32", DefaultParams().Factors)
	}
	for name, mutate := range map[string]func(*Params){
		"no factors":      func(p *Params) { p.Factors = 0 },
		"too many":        func(p *Params) { p.Factors = MaxFactors + 1 },
		"zero reg":        func(p *Params) { p.Regularization = 0 },
		"nan reg":         func(p *Params) { p.Regularization = math.NaN() },
		"zero alpha":      func(p *Params) { p.Alpha = 0 },
		"no iterations":   func(p *Params) { p.Iterations = 0 },
		"too many sweeps": func(p *Params) { p.Iterations = MaxIterations + 1 },
	} {
		p := DefaultParams()
		mutate(&p)
		if p.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The fast solve counts every untouched pair through the Gram matrix. It must equal the plain
// normal equations over all items, written out the long way.
func TestFoldInMatchesDenseNormalEquations(t *testing.T) {
	const items, f = 6, 3
	p := Params{Factors: f, Regularization: 0.3, Alpha: 4, Iterations: 1, Seed: 1}
	rng := rand.New(rand.NewPCG(3, 5))
	vecs := make([]float32, items*f)
	for i := range vecs {
		vecs[i] = float32(rng.NormFloat64())
	}
	m, err := NewModel(p, ids(items), vecs)
	if err != nil {
		t.Fatal(err)
	}
	history := []Entry{{Row: 1, Value: 2}, {Row: 4, Value: 0.5}}
	fit, err := m.FoldIn(history)
	if err != nil {
		t.Fatal(err)
	}

	// dense: A = Σ_i c_i b_i b_iᵀ + λI, v = Σ_observed c_i b_i, c_i = 1 + α x_i (1 if untouched)
	strength := map[int]float64{1: 2, 4: 0.5}
	A := make([][]float64, f)
	for i := range A {
		A[i] = make([]float64, f)
		A[i][i] = p.Regularization
	}
	v := make([]float64, f)
	for i := 0; i < items; i++ {
		c := 1 + p.Alpha*strength[i]
		for r := 0; r < f; r++ {
			for s := 0; s < f; s++ {
				A[r][s] += c * float64(vecs[i*f+r]) * float64(vecs[i*f+s])
			}
			if _, seen := strength[i]; seen {
				v[r] += c * float64(vecs[i*f+r])
			}
		}
	}
	want := gaussSolve(A, v)
	for k := range want {
		if !near(fit.vec[k], want[k], 1e-9) {
			t.Fatalf("fold-in = %v, dense solve = %v", fit.vec, want)
		}
	}
}

func gaussSolve(a [][]float64, b []float64) []float64 {
	n := len(b)
	for i := 0; i < n; i++ {
		piv := i
		for r := i + 1; r < n; r++ {
			if math.Abs(a[r][i]) > math.Abs(a[piv][i]) {
				piv = r
			}
		}
		a[i], a[piv], b[i], b[piv] = a[piv], a[i], b[piv], b[i]
		for r := i + 1; r < n; r++ {
			m := a[r][i] / a[i][i]
			for c := i; c < n; c++ {
				a[r][c] -= m * a[i][c]
			}
			b[r] -= m * b[i]
		}
	}
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		s := b[i]
		for c := i + 1; c < n; c++ {
			s -= a[i][c] * x[c]
		}
		x[i] = s / a[i][i]
	}
	return x
}

// The data has two tastes, so two factors describe it exactly. Extra factors start to fit the
// sampling noise (each user likes a random six of their ten), which blurs items of one taste
// apart; stronger regularisation takes that back. This is why the factor count and the
// regularisation are tuned together on held-out data rather than set as large as possible.
func TestTrainLearnsWhichItemsGoTogether(t *testing.T) {
	items, users, obs := twoTastes()
	for _, tc := range []struct {
		name           string
		factors        int
		regularisation float64
		minWithin      float64
	}{
		{"as many factors as tastes", 2, 0.1, 0.95},
		{"more factors, stronger regularisation", 4, 1, 0.8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := DefaultParams()
			p.Factors, p.Regularization = tc.factors, tc.regularisation
			m, err := Train(items, users, obs, p, epoch)
			if err != nil {
				t.Fatal(err)
			}
			if m.Users != 40 || m.Interactions != len(obs) || !m.TrainedAt.Equal(epoch) {
				t.Errorf("model = users %d, interactions %d, trained %v", m.Users, m.Interactions, m.TrainedAt)
			}
			var within, across float64
			var nWithin, nAcross int
			for a := 0; a < 20; a++ {
				for b := a + 1; b < 20; b++ {
					if (a < 10) == (b < 10) {
						within += m.Cosine(a, b)
						nWithin++
					} else {
						across += m.Cosine(a, b)
						nAcross++
					}
				}
			}
			within, across = within/float64(nWithin), across/float64(nAcross)
			if within < tc.minWithin || across > 0.1 {
				t.Errorf("mean cosine within a taste %.2f, across %.2f; want above %.2f and below 0.1", within, across, tc.minWithin)
			}
		})
	}
}

func TestFoldInRecommendsTheUsersOwnTaste(t *testing.T) {
	items, users, obs := twoTastes()
	p := DefaultParams()
	p.Factors = 4
	m, err := Train(items, users, obs, p, epoch)
	if err != nil {
		t.Fatal(err)
	}
	// a user who is not in the data and liked items 10, 11, 12: all from the second taste
	fit, err := m.FoldIn([]Entry{{Row: 10, Value: 1}, {Row: 11, Value: 1}, {Row: 12, Value: 1}})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{10: true, 11: true, 12: true}
	top := fit.Top(5, func(row int) bool { return seen[row] })
	if len(top) != 5 {
		t.Fatalf("top = %v, want 5 rows", top)
	}
	for _, row := range top {
		if row < 10 {
			t.Errorf("row %d belongs to the other taste; top = %v", row, top)
		}
	}
	if fit.Score(15) <= fit.Score(3) {
		t.Errorf("a same-taste item scores %.3f, an other-taste item %.3f", fit.Score(15), fit.Score(3))
	}
}

func TestContributionsSumToTheScore(t *testing.T) {
	items, users, obs := twoTastes()
	p := DefaultParams()
	p.Factors = 6
	m, err := Train(items, users, obs, p, epoch)
	if err != nil {
		t.Fatal(err)
	}
	fit, err := m.FoldIn([]Entry{{Row: 2, Value: 1}, {Row: 5, Value: 0.4}, {Row: 12, Value: 2}})
	if err != nil {
		t.Fatal(err)
	}
	for row := 0; row < 20; row++ {
		var sum float64
		parts := fit.Contributions(row)
		for _, c := range parts {
			sum += c.Value
		}
		if !near(sum, fit.Score(row), 1e-9) {
			t.Errorf("row %d: contributions sum to %.12f, score is %.12f", row, sum, fit.Score(row))
		}
		if len(parts) != 3 {
			t.Errorf("row %d: %d contributions, want 3 (one per history item)", row, len(parts))
		}
		if !slices.IsSortedFunc(parts, func(a, b Contribution) int {
			switch {
			case a.Value > b.Value:
				return -1
			case a.Value < b.Value:
				return 1
			}
			return 0
		}) {
			t.Errorf("row %d: contributions are not largest first: %v", row, parts)
		}
	}
	// the explanation points at the items of the same taste
	best := fit.Contributions(11)[0]
	if best.Row != 12 {
		t.Errorf("item 11 is explained by row %d, want 12 (its taste, and the strongest reaction)", best.Row)
	}
}

func TestHistoryEdgeCases(t *testing.T) {
	items, users, obs := twoTastes()
	p := DefaultParams()
	p.Factors = 4
	m, _ := Train(items, users, obs, p, epoch)

	for name, history := range map[string][]Entry{
		"none":             nil,
		"unknown rows":     {{Row: -1, Value: 1}, {Row: 99, Value: 1}},
		"no strength":      {{Row: 1, Value: 0}, {Row: 2, Value: -3}},
		"not a real value": {{Row: 1, Value: math.Inf(1)}},
	} {
		fit, err := m.FoldIn(history)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !fit.Empty() || fit.Score(3) != 0 || fit.Top(5, nil) != nil || fit.Contributions(3) != nil {
			t.Errorf("%s: an empty history must score everything 0 and explain nothing", name)
		}
	}

	// repeated rows add up: the same as one entry with the sum
	a, _ := m.FoldIn([]Entry{{Row: 3, Value: 1}, {Row: 3, Value: 1.5}})
	b, _ := m.FoldIn([]Entry{{Row: 3, Value: 2.5}})
	if !near(a.Score(4), b.Score(4), 1e-12) {
		t.Errorf("repeated entries score %.9f, one summed entry %.9f", a.Score(4), b.Score(4))
	}
}

func TestTrainIsReproducible(t *testing.T) {
	items, users, obs := twoTastes()
	p := DefaultParams()
	p.Factors = 5
	one, _ := Train(items, users, obs, p, epoch)
	two, _ := Train(items, users, obs, p, epoch)
	if !slices.Equal(one.Vecs, two.Vecs) || !slices.Equal(one.Gram, two.Gram) || one.Loss != two.Loss {
		t.Error("the same data and seed gave different models")
	}
	p.Seed = 99
	other, _ := Train(items, users, obs, p, epoch)
	if slices.Equal(one.Vecs, other.Vecs) {
		t.Error("another seed gave the same vectors")
	}
	// input order does not matter either
	shuffled := slices.Clone(obs)
	rand.New(rand.NewPCG(1, 1)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	again, _ := Train(items, users, shuffled, DefaultParams(), epoch)
	base, _ := Train(items, users, obs, DefaultParams(), epoch)
	if !slices.Equal(again.Vecs, base.Vecs) {
		t.Error("the order of the observations changed the model")
	}
}

// Each half-step minimises the objective with the other side fixed, so a sweep never raises it.
func TestEverySweepLowersTheLoss(t *testing.T) {
	items, users, obs := twoTastes()
	p := DefaultParams()
	p.Factors, p.Iterations = 4, 12
	var losses []float64
	if _, err := train(items, users, obs, p, epoch, func(_ int, l float64) { losses = append(losses, l) }); err != nil {
		t.Fatal(err)
	}
	if len(losses) != 12 {
		t.Fatalf("got %d sweeps", len(losses))
	}
	for i := 1; i < len(losses); i++ {
		if losses[i] > losses[i-1]*(1+1e-9) {
			t.Errorf("sweep %d raised the loss: %.6f -> %.6f", i+1, losses[i-1], losses[i])
		}
	}
	if losses[len(losses)-1] >= losses[0] {
		t.Errorf("training did not improve: %.4f -> %.4f", losses[0], losses[len(losses)-1])
	}
}

func TestTrainErrors(t *testing.T) {
	items := ids(3)
	if _, err := Train(items, 2, nil, DefaultParams(), epoch); !errors.Is(err, ErrNoData) {
		t.Errorf("no observations: %v, want ErrNoData", err)
	}
	if _, err := Train(items, 2, []Obs{{User: 0, Item: 1, Value: 0}, {User: 1, Item: 2, Value: -1}}, DefaultParams(), epoch); !errors.Is(err, ErrNoData) {
		t.Errorf("nothing positive: %v, want ErrNoData", err)
	}
	if _, err := Train(items, 2, []Obs{{User: 5, Item: 0, Value: 1}}, DefaultParams(), epoch); err == nil {
		t.Error("a user outside the range was accepted")
	}
	if _, err := Train(items, 2, []Obs{{User: 0, Item: 3, Value: 1}}, DefaultParams(), epoch); err == nil {
		t.Error("an item outside the range was accepted")
	}
	bad := DefaultParams()
	bad.Alpha = 0
	if _, err := Train(items, 2, []Obs{{User: 0, Item: 0, Value: 1}}, bad, epoch); err == nil {
		t.Error("invalid params were accepted")
	}
}

func TestRepeatedObservationsAddUp(t *testing.T) {
	items := ids(4)
	p := DefaultParams()
	p.Factors = 2
	split, _ := Train(items, 2, []Obs{{0, 0, 1}, {0, 0, 1}, {0, 1, 1}, {1, 1, 1}, {1, 2, 1}, {1, 3, 1}}, p, epoch)
	whole, _ := Train(items, 2, []Obs{{0, 0, 2}, {0, 1, 1}, {1, 1, 1}, {1, 2, 1}, {1, 3, 1}}, p, epoch)
	if !slices.Equal(split.Vecs, whole.Vecs) || split.Interactions != 5 {
		t.Errorf("pairs given twice must equal one pair with the sum; interactions = %d", split.Interactions)
	}
}

func TestModelRowsAndShape(t *testing.T) {
	if _, err := NewModel(DefaultParams(), ids(2), make([]float32, 10)); err == nil {
		t.Error("vectors of the wrong length were accepted")
	}
	p := Params{Factors: 2, Regularization: 1, Alpha: 1, Iterations: 1}
	m, err := NewModel(p, ids(3), []float32{1, 0, 0, 1, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	if row, ok := m.Row("i01"); !ok || row != 1 {
		t.Errorf("Row(i01) = %d, %v", row, ok)
	}
	if _, ok := m.Row("nope"); ok {
		t.Error("an unknown item has a row")
	}
	if !near(m.Cosine(0, 1), 0, 1e-12) || !near(m.Cosine(0, 2), 1/math.Sqrt2, 1e-6) {
		t.Errorf("cosines = %v, %v", m.Cosine(0, 1), m.Cosine(0, 2))
	}
	// Gram of [[1 0] [0 1] [1 1]] is [[2 1] [1 2]]
	if !slices.Equal(m.Gram, []float64{2, 1, 1, 2}) {
		t.Errorf("gram = %v", m.Gram)
	}
	zero, _ := NewModel(p, ids(2), make([]float32, 4))
	if zero.Cosine(0, 1) != 0 {
		t.Error("the cosine with a zero vector must be 0")
	}
}
