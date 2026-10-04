package geo

import (
	"math"
	"testing"

	"github.com/timurcravtov/walrus/internal/domain"
)

func point(t *testing.T, lat, lon float64) domain.Value {
	t.Helper()
	v, err := Parse(map[string]any{"lat": lat, "lon": lon})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestKm(t *testing.T) {
	chisinau, bucharest := point(t, 47.0105, 28.8638), point(t, 44.4268, 26.1025)
	d, ok := Km(chisinau, bucharest)
	if !ok || math.Abs(d-358) > 3 {
		t.Errorf("Chisinau to Bucharest = %.1f km, want about 358", d)
	}
	if d, _ := Km(chisinau, chisinau); d > 1e-6 {
		t.Errorf("a point is %v km from itself", d)
	}
	back, _ := Km(bucharest, chisinau)
	if math.Abs(back-d) > 1e-6 && d != 0 {
		t.Errorf("distance should not depend on direction")
	}
	if near, _ := Km(point(t, 47.0, 28.8), point(t, 47.01, 28.8)); math.Abs(near-1.11) > 0.02 {
		t.Errorf("0.01 degrees of latitude = %.3f km, want about 1.11", near)
	}
	// across the date line and over the pole the short way round is taken
	if d, _ := Km(point(t, 0, 179.5), point(t, 0, -179.5)); math.Abs(d-111.2) > 1 {
		t.Errorf("across the date line = %.1f km, want about 111", d)
	}
	if _, ok := Km(chisinau, domain.Num(1)); ok {
		t.Error("a number is not a point")
	}
	if _, ok := Km(chisinau, domain.Null()); ok {
		t.Error("nothing is not a point")
	}
}

func TestParse(t *testing.T) {
	for name, raw := range map[string]any{
		"not an object": "47,28",
		"no lon":        map[string]any{"lat": 1.0},
		"string lat":    map[string]any{"lat": "1", "lon": 2.0},
		"extra key":     map[string]any{"lat": 1.0, "lon": 2.0, "alt": 3.0},
		"lat too big":   map[string]any{"lat": 91.0, "lon": 0.0},
		"lon too big":   map[string]any{"lat": 0.0, "lon": -181.0},
	} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	v, err := Parse(map[string]any{"lat": 47.5, "lon": 28.25})
	if vec, ok := v.AsVector(); err != nil || !ok || len(vec) != 2 || vec[0] != 47.5 || vec[1] != 28.25 {
		t.Errorf("%v %v", v, err)
	}
}
