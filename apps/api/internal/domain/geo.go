package domain

import "math"

// Sumber koordinat scan (scan_events.geo_source). GPS dari browser lebih
// dipercaya daripada perkiraan dari IP; NONE = tidak ada koordinat.
const (
	GeoGPS  = "GPS"
	GeoIP   = "IP"
	GeoNone = "NONE"
)

// GeoPoint adalah koordinat WGS84 dalam derajat.
type GeoPoint struct {
	Lat float64
	Lng float64
}

// Valid melaporkan apakah titik berada di rentang lintang/bujur yang sah.
func (p GeoPoint) Valid() bool {
	return p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180 &&
		!math.IsNaN(p.Lat) && !math.IsNaN(p.Lng)
}

const earthRadiusKm = 6371.0088

// HaversineKm menghitung jarak lingkaran besar antara dua titik dalam km.
func HaversineKm(a, b GeoPoint) float64 {
	const rad = math.Pi / 180
	dLat := (b.Lat - a.Lat) * rad
	dLng := (b.Lng - a.Lng) * rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusKm * math.Asin(math.Min(1, math.Sqrt(h)))
}
