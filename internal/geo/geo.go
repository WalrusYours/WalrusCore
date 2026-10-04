// Package geo is the point type of the schema language: a latitude and longitude, kept as a
// two-number vector so it needs no value kind of its own, and the distance between two points.
package geo

import (
	"fmt"
	"math"

	"github.com/timurcravtov/walrus/internal/domain"
)

const earthRadiusKm = 6371.0088

// Parse reads {"lat": 47.01, "lon": 28.86}.
func Parse(raw any) (domain.Value, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return domain.Null(), fmt.Errorf(`expected {"lat": <number>, "lon": <number>}, got %T`, raw)
	}
	for k := range m {
		if k != "lat" && k != "lon" {
			return domain.Null(), fmt.Errorf("a point has lat and lon, not %q", k)
		}
	}
	lat, okLat := m["lat"].(float64)
	lon, okLon := m["lon"].(float64)
	switch {
	case !okLat || !okLon:
		return domain.Null(), fmt.Errorf(`expected {"lat": <number>, "lon": <number>}`)
	case lat < -90 || lat > 90:
		return domain.Null(), fmt.Errorf("lat %v is outside [-90, 90]", lat)
	case lon < -180 || lon > 180:
		return domain.Null(), fmt.Errorf("lon %v is outside [-180, 180]", lon)
	}
	return domain.Vec(float32(lat), float32(lon)), nil
}

// Km is the great-circle distance between two points, or false when either is not a point.
func Km(a, b domain.Value) (float64, bool) {
	pa, okA := a.AsVector()
	pb, okB := b.AsVector()
	if !okA || !okB || len(pa) != 2 || len(pb) != 2 {
		return 0, false
	}
	lat1, lat2 := rad(float64(pa[0])), rad(float64(pb[0]))
	dLat := lat2 - lat1
	dLon := rad(float64(pb[1])) - rad(float64(pa[1]))
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusKm * math.Asin(math.Min(1, math.Sqrt(h))), true
}

func rad(deg float64) float64 { return deg * math.Pi / 180 }
