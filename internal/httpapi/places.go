package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	// placesUpstream is where the proxies send their requests unless a test
	// points them elsewhere.
	placesUpstream = "https://places.googleapis.com"

	// placesFieldMask is owned by the gateway, not the caller. Places bills by
	// the highest field tier requested, so a caller-supplied list would let
	// anyone on the internet choose the expensive fields on this key. The map
	// bubble needs exactly these (PLAN.md §63).
	placesFieldMask = "places.id,places.displayName,places.formattedAddress," +
		"places.internationalPhoneNumber,places.websiteUri,places.googleMapsUri," +
		"places.rating,places.userRatingCount,places.priceLevel," +
		"places.primaryTypeDisplayName,places.editorialSummary," +
		"places.regularOpeningHours,places.utcOffsetMinutes," +
		"places.reviews,places.photos"

	// placesCallTimeout bounds one upstream call. The default http client has
	// no timeout, so a stalled Google response would hold a request open.
	placesCallTimeout = 8 * time.Second

	// placesSearchHalfSpan is how far, in degrees of latitude and longitude,
	// the search reaches from the pin. About 1.6 km: far enough for a Qloo
	// point to sit off its Google twin, near enough that a same-named place
	// in another part of the city is not returned instead.
	placesSearchHalfSpan = 0.015

	maxReviewRunes = 400
	maxReviews     = 5
	maxPhotos      = 5
)

// placesPhotoName is the only shape of photo reference the proxy forwards. The
// name is interpolated into a URL that carries the gateway's API key, so
// anything outside this shape (a `..` segment, a different resource) is refused
// before a request is made.
var placesPhotoName = regexp.MustCompile(`^places/[A-Za-z0-9_-]+/photos/[A-Za-z0-9_-]+$`)

type placesText struct {
	Text string `json:"text"`
}

type placesAttribution struct {
	DisplayName string `json:"displayName"`
}

type placesPlace struct {
	ID                     string     `json:"id"`
	DisplayName            placesText `json:"displayName"`
	FormattedAddress       string     `json:"formattedAddress"`
	InternationalPhone     string     `json:"internationalPhoneNumber"`
	WebsiteURI             string     `json:"websiteUri"`
	GoogleMapsURI          string     `json:"googleMapsUri"`
	Rating                 float64    `json:"rating"`
	UserRatingCount        int        `json:"userRatingCount"`
	PriceLevel             string     `json:"priceLevel"`
	PrimaryTypeDisplayName placesText `json:"primaryTypeDisplayName"`
	EditorialSummary       placesText `json:"editorialSummary"`
	UTCOffsetMinutes       *int       `json:"utcOffsetMinutes"`
	RegularOpeningHours    struct {
		OpenNow             *bool    `json:"openNow"`
		WeekdayDescriptions []string `json:"weekdayDescriptions"`
	} `json:"regularOpeningHours"`
	Reviews []struct {
		Rating                         float64           `json:"rating"`
		RelativePublishTimeDescription string            `json:"relativePublishTimeDescription"`
		Text                           placesText        `json:"text"`
		AuthorAttribution              placesAttribution `json:"authorAttribution"`
	} `json:"reviews"`
	Photos []struct {
		Name               string              `json:"name"`
		AuthorAttributions []placesAttribution `json:"authorAttributions"`
	} `json:"photos"`
}

// placeDetailResult is the shape the client reads. Keys are snake_case and
// every Google-sourced value stays a Google fact on the client.
type placeDetailResult struct {
	Name         string        `json:"name"`
	Address      string        `json:"formatted_address,omitempty"`
	Phone        string        `json:"formatted_phone_number,omitempty"`
	Website      string        `json:"website,omitempty"`
	MapsURL      string        `json:"maps_url,omitempty"`
	Rating       float64       `json:"rating,omitempty"`
	RatingCount  int           `json:"rating_count,omitempty"`
	PriceLevel   int           `json:"price_level,omitempty"`
	Category     string        `json:"category,omitempty"`
	Summary      string        `json:"summary,omitempty"`
	OpenNow      *bool         `json:"open_now,omitempty"`
	UTCOffsetMin *int          `json:"utc_offset_minutes,omitempty"`
	OpeningHours []string      `json:"opening_hours,omitempty"`
	Reviews      []placeReview `json:"reviews,omitempty"`
	Photos       []placePhoto  `json:"photos,omitempty"`
}

type placeReview struct {
	Author       string  `json:"author"`
	Rating       float64 `json:"rating,omitempty"`
	RelativeTime string  `json:"relative_time,omitempty"`
	Text         string  `json:"text"`
}

type placePhoto struct {
	Name   string `json:"name"`
	Author string `json:"author,omitempty"`
}

// priceLevelRank turns Google's enum into 1 (inexpensive) through 4 (very
// expensive); free and unspecified levels return 0 and are omitted.
func priceLevelRank(level string) int {
	switch level {
	case "PRICE_LEVEL_INEXPENSIVE":
		return 1
	case "PRICE_LEVEL_MODERATE":
		return 2
	case "PRICE_LEVEL_EXPENSIVE":
		return 3
	case "PRICE_LEVEL_VERY_EXPENSIVE":
		return 4
	}
	return 0
}

func clipRunes(text string, limit int) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

func toPlaceDetailResult(place placesPlace) placeDetailResult {
	result := placeDetailResult{
		Name:         place.DisplayName.Text,
		Address:      place.FormattedAddress,
		Phone:        place.InternationalPhone,
		Website:      place.WebsiteURI,
		MapsURL:      place.GoogleMapsURI,
		Rating:       place.Rating,
		RatingCount:  place.UserRatingCount,
		PriceLevel:   priceLevelRank(place.PriceLevel),
		Category:     place.PrimaryTypeDisplayName.Text,
		Summary:      place.EditorialSummary.Text,
		OpenNow:      place.RegularOpeningHours.OpenNow,
		UTCOffsetMin: place.UTCOffsetMinutes,
		OpeningHours: place.RegularOpeningHours.WeekdayDescriptions,
	}
	for _, review := range place.Reviews {
		text := clipRunes(review.Text.Text, maxReviewRunes)
		if text == "" || len(result.Reviews) >= maxReviews {
			continue
		}
		result.Reviews = append(result.Reviews, placeReview{
			Author:       review.AuthorAttribution.DisplayName,
			Rating:       review.Rating,
			RelativeTime: review.RelativePublishTimeDescription,
			Text:         text,
		})
	}
	for _, photo := range place.Photos {
		if photo.Name == "" || len(result.Photos) >= maxPhotos {
			continue
		}
		entry := placePhoto{Name: photo.Name}
		if len(photo.AuthorAttributions) > 0 {
			entry.Author = photo.AuthorAttributions[0].DisplayName
		}
		result.Photos = append(result.Photos, entry)
	}
	return result
}

// validPin reports whether a latitude/longitude pair names a real place. Zero
// is rejected because the client sends it for "no point".
func validPin(lat, lng float64) bool {
	if math.IsNaN(lat) || math.IsNaN(lng) || math.IsInf(lat, 0) || math.IsInf(lng, 0) {
		return false
	}
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return false
	}
	return lat != 0 || lng != 0
}

func (h *Handler) placesBaseURL() string {
	if h.placesBase != "" {
		return h.placesBase
	}
	return placesUpstream
}

var placesHTTP = &http.Client{Timeout: placesCallTimeout}

// getPlaceDetails proxies a Google Places (New) Text Search request through the
// gateway so the browser client does not have to deal with CORS, and so the
// API key and the field mask stay on the server.
//
// `lat` and `lng`, when present, restrict the search to a box around the pin:
// a name alone ("Whisler's") matches places in other cities, and the bubble
// would then show the wrong venue's phone number. A `fields` parameter, which
// the first client version sent, is ignored on purpose.
func (h *Handler) getPlaceDetails(c *gin.Context) {
	key := h.cfg.PlacesAPIKey
	if key == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "not_configured",
			"message": "GOOGLE_PLACES_API_KEY is not set on the gateway",
		})
		return
	}
	if !h.placesLimiter.Allow(c.ClientIP()) {
		writeError(c, http.StatusTooManyRequests, "rate_limited", "too many place lookups; retry shortly")
		return
	}

	placeName := strings.TrimSpace(c.Query("place_name"))
	if placeName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "bad_request",
			"message": "place_name query parameter is required",
		})
		return
	}
	if utf8.RuneCountInString(placeName) > 200 {
		writeError(c, http.StatusBadRequest, "validation", "place_name is too long")
		return
	}

	body := map[string]any{"textQuery": placeName, "pageSize": 1}
	lat, latErr := strconv.ParseFloat(c.Query("lat"), 64)
	lng, lngErr := strconv.ParseFloat(c.Query("lng"), 64)
	if latErr == nil && lngErr == nil && validPin(lat, lng) {
		body["locationRestriction"] = map[string]any{
			"rectangle": map[string]any{
				"low":  map[string]float64{"latitude": lat - placesSearchHalfSpan, "longitude": lng - placesSearchHalfSpan},
				"high": map[string]float64{"latitude": lat + placesSearchHalfSpan, "longitude": lng + placesSearchHalfSpan},
			},
		}
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), placesCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.placesBaseURL()+"/v1/places:searchText", strings.NewReader(string(jsonBody)))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", key)
	req.Header.Set("X-Goog-FieldMask", placesFieldMask)

	resp, err := placesHTTP.Do(req)
	if err != nil {
		// The transport error is not echoed: it can carry the request URL.
		writeError(c, http.StatusBadGateway, "upstream", "the places lookup failed")
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		writeError(c, http.StatusBadGateway, "upstream", "the places lookup failed")
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeError(c, http.StatusBadGateway, "upstream", "the places lookup was refused")
		return
	}

	var data struct {
		Places []placesPlace `json:"places"`
	}
	if err := json.Unmarshal(respBody, &data); err != nil {
		writeError(c, http.StatusBadGateway, "upstream", "the places lookup returned an unreadable answer")
		return
	}
	if len(data.Places) == 0 {
		c.JSON(http.StatusOK, gin.H{"status": "NOT_FOUND", "result": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "OK", "result": toPlaceDetailResult(data.Places[0])})
}

// getPlacePhoto proxies a Google Places (New) Photo request and returns the
// image as a base64 data URI so the client can hand it to Image.network.
func (h *Handler) getPlacePhoto(c *gin.Context) {
	key := h.cfg.PlacesAPIKey
	if key == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "not_configured",
			"message": "GOOGLE_PLACES_API_KEY is not set on the gateway",
		})
		return
	}
	if !h.placesLimiter.Allow(c.ClientIP()) {
		writeError(c, http.StatusTooManyRequests, "rate_limited", "too many photo lookups; retry shortly")
		return
	}

	photoName := c.Query("photo_name")
	if photoName == "" {
		// Fall back to legacy photo_reference param for compatibility.
		photoName = c.Query("photo_reference")
	}
	if !placesPhotoName.MatchString(photoName) {
		writeError(c, http.StatusBadRequest, "validation", "photo_name is not a places photo reference")
		return
	}

	maxWidth := 400
	if raw := c.Query("maxwidth"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 100 || parsed > 1200 {
			writeError(c, http.StatusBadRequest, "validation", "maxwidth must be between 100 and 1200")
			return
		}
		maxWidth = parsed
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), placesCallTimeout)
	defer cancel()
	mediaURL := h.placesBaseURL() + "/v1/" + photoName + "/media?maxWidthPx=" + url.QueryEscape(strconv.Itoa(maxWidth))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	// The key travels in a header, not the URL, so a failed call cannot echo it.
	req.Header.Set("X-Goog-Api-Key", key)

	resp, err := placesHTTP.Do(req)
	if err != nil {
		writeError(c, http.StatusBadGateway, "upstream", "the photo lookup failed")
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		writeError(c, http.StatusBadGateway, "upstream", "the photo lookup failed")
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeError(c, http.StatusBadGateway, "upstream", "the photo lookup was refused")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"photo_data": "data:" + resp.Header.Get("Content-Type") + ";base64," + base64.StdEncoding.EncodeToString(body),
	})
}
