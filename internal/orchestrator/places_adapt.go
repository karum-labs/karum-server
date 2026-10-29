package orchestrator

import "github.com/karum-labs/karum-server/internal/places"

func toPlaceTargets(targets []placesTarget) []places.Target {
	out := make([]places.Target, len(targets))
	for i, target := range targets {
		out[i] = places.Target{
			ID: target.ID, Name: target.Name, City: target.City,
			Lat: target.Lat, Lng: target.Lng,
		}
	}
	return out
}

func fromPlaceRows(rows []places.Enrichment) []Enrichment {
	out := make([]Enrichment, len(rows))
	for i, row := range rows {
		out[i] = Enrichment{
			ID: row.ID, Address: row.Address, Hours: row.Hours, Website: row.Website,
			Phone: row.Phone, OpenStatus: row.OpenStatus, Lat: row.Lat, Lng: row.Lng,
			Closed: row.Closed, Unknown: row.Unknown, Attribution: row.Attribution,
		}
	}
	return out
}
