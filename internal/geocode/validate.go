package geocode

import "math"

// ValidatePoint rejects coordinates the map, the scout radius, and Places
// cannot use. Latitude must sit in [-90, 90] and longitude in [-180, 180].
func ValidatePoint(lat, lng float64) error {
	if math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
		return &ValidationError{Message: "latitude is unusable"}
	}
	if math.IsNaN(lng) || math.IsInf(lng, 0) || lng < -180 || lng > 180 {
		return &ValidationError{Message: "longitude is unusable"}
	}
	return nil
}
