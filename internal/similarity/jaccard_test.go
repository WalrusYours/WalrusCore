package similarity

import (
	"math"
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
