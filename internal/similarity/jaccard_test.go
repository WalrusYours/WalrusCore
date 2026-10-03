package similarity

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/timurcravtov/walrus/internal/domain"
)

func TestJaccard(t *testing.T) {
	tests := []struct {
		name string
		a, b domain.Value
		want float64
	}{
		{"partial overlap", domain.Set("rock", "indie"), domain.Set("rock", "pop"), 1.0 / 3},
		{"identical", domain.Set("rock", "pop"), domain.Set("pop", "rock"), 1},
		{"disjoint", domain.Set("rock"), domain.Set("jazz"), 0},
		{"both empty", domain.Set(), domain.Set(), 0},
		{"one empty", domain.Set("rock"), domain.Set(), 0},
		{"duplicates collapse", domain.Set("rock", "rock"), domain.Set("rock"), 1},
		{"not a set", domain.Num(1), domain.Set("rock"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Jaccard(tt.a, tt.b); math.Abs(got-tt.want) > 1e-12 {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func randomSets(n, vocab, size int, r *rand.Rand) []domain.Value {
	out := make([]domain.Value, n)
	for i := range out {
		m := make([]string, size)
		for j := range m {
			m[j] = fmt.Sprintf("g%d", r.Intn(vocab))
		}
		out[i] = domain.Set(m...)
	}
	return out
}

func TestIndexPicksRepresentation(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	if !NewJaccardIndex(randomSets(50, 100, 5, r)).Bitset() {
		t.Fatal("100-member vocabulary should use bitsets")
	}
	if NewJaccardIndex(randomSets(500, 5000, 20, r)).Bitset() {
		t.Fatal("large vocabulary should fall back to sorted slices")
	}
}

// Both representations must agree with the plain Jaccard function.
func TestIndexMatchesJaccard(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for _, vocab := range []int{40, 130, 5000} { // 1 word, 3 words, fallback
		sets := randomSets(30, vocab, 12, r)
		x := NewJaccardIndex(sets)
		for i := range sets {
			for j := range sets {
				got, want := x.Score(i, j), Jaccard(sets[i], sets[j])
				if math.Abs(got-want) > 1e-12 {
					t.Fatalf("vocab %d, (%d,%d): got %v, want %v", vocab, i, j, got, want)
				}
			}
		}
	}
}

func TestTop(t *testing.T) {
	x := NewJaccardIndex([]domain.Value{
		domain.Set("rock", "indie"), // seed
		domain.Set("rock", "indie"), // 1.0
		domain.Set("rock", "pop"),   // 1/3
		domain.Set("jazz"),          // 0, dropped
	})
	got := x.Top(0, 5)
	if len(got) != 2 || got[0].Index != 1 || got[1].Index != 2 {
		t.Fatalf("unexpected order: %+v", got)
	}
	if got := x.Top(0, 1); len(got) != 1 {
		t.Fatalf("k not applied: %+v", got)
	}
}

func BenchmarkScore(b *testing.B) {
	for _, vocab := range []int{200, 5000} {
		sets := randomSets(1000, vocab, 8, rand.New(rand.NewSource(3)))
		x := NewJaccardIndex(sets)
		b.Run(fmt.Sprintf("vocab%d_bitset%v", vocab, x.Bitset()), func(b *testing.B) {
			for i := range b.N {
				x.Score(i%1000, (i*7+1)%1000)
			}
		})
	}
}
