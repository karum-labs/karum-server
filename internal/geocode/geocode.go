// Package geocode resolves a city query to one geographic point.
//
// The gateway geocodes the city before any taste RPC. An ambiguous query
// returns candidates and does not pick one. An empty query and unusable
// coordinates are validation errors. Successful lookups are cached. Preset
// campaigns skip this package and post their stored points (PLAN.md §20).
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

// Result is the outcome of geocoding a single city query.
type Result struct {
	// Resolved is the chosen point when the query was unambiguous.
	Resolved Point
	// FormattedAddress is the provider's display string for Resolved.
	FormattedAddress string
	// Candidates are populated when the query was ambiguous. Resolved is then
	// the zero point; the caller must not pick one.
	Candidates []Candidate
	// Attribution is the provider credit the client is required to show.
	Attribution string
	// CacheHit is true when the result was served from the geocode cache.
	CacheHit bool
}

// ValidationError is a city query or a coordinate pair the gateway must
// reject before any taste RPC. Callers map it onto the validation error code.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	if e == nil || e.Message == "" {
		return "validation"
	}
	return e.Message
}

// AttributionGoogle is the credit stored beside a Google geocode.
const AttributionGoogle = "Map data © Google"

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
