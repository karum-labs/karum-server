// Package geocode resolves a vacancy address to a geographic point.
//
// This is a skeleton. The real implementation calls the geocoding provider,
// returns candidate points when the address is ambiguous rather than guessing,
// rejects unusable coordinates with a validation error, and caches results.
// The single resolved lat/lng is reused for the map pin, the saturation radius,
// and the Qloo location filter. See PLAN.md epic 5 for the full contract.
package geocode

import (
	"context"
	"errors"
)

// Point is a resolved latitude/longitude pair.
type Point struct {
	// Lat is the latitude in decimal degrees.
	Lat float64
	// Lng is the longitude in decimal degrees.
	Lng float64
}

// Candidate is one possible interpretation of an ambiguous address. The client
// shows these so the user confirms a point instead of us picking one.
type Candidate struct {
	// FormattedAddress is the provider's display string for this point.
	FormattedAddress string
	// Point is the coordinates for this candidate.
	Point Point
}

// Result is the outcome of geocoding a single address.
type Result struct {
	// Resolved is the chosen point when the address was unambiguous.
	Resolved Point
	// Candidates are populated when the address was ambiguous, so the caller can
	// pause and ask the user to pick.
	Candidates []Candidate
}

// Geocoder resolves addresses to points.
type Geocoder interface {
	// Geocode resolves an address. An ambiguous address returns Candidates and
	// leaves Resolved as the zero value; an unresolvable address returns an
	// error carrying the "validation" code.
	Geocode(ctx context.Context, address string) (*Result, error)
}

// NoopGeocoder is a placeholder Geocoder that always fails. It exists so the
// gateway compiles and its tests run before a real provider is wired up.
type NoopGeocoder struct{}

// Geocode implements Geocoder. It always returns an error because no provider
// is configured yet.
func (NoopGeocoder) Geocode(ctx context.Context, address string) (*Result, error) {
	_ = ctx
	_ = address
	return nil, ErrNotImplemented
}

// ErrNotImplemented signals that no geocoding provider has been wired up yet.
var ErrNotImplemented = errors.New("geocoder not implemented: no provider configured")
