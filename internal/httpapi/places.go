package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// getPlaceDetails proxies a Google Places (New) Text Search request through the
// gateway so the browser client does not have to deal with CORS.
func (h *Handler) getPlaceDetails(c *gin.Context) {
	key := h.cfg.PlacesAPIKey
	if key == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "not_configured",
			"message": "GOOGLE_PLACES_API_KEY is not set on the gateway",
		})
		return
	}

	placeName := c.Query("place_name")
	if placeName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "bad_request",
			"message": "place_name query parameter is required",
		})
		return
	}

	// Build the request body for the new Places API Text Search.
	body := map[string]any{
		"textQuery": placeName,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}

	req, err := http.NewRequest("POST", "https://places.googleapis.com/v1/places:searchText", strings.NewReader(string(jsonBody)))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", key)
	req.Header.Set("X-Goog-FieldMask", "places.id,places.displayName,places.formattedAddress,places.internationalPhoneNumber,places.rating,places.reviews,places.photos")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":   "upstream_error",
			"message": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":   "upstream_error",
			"message": err.Error(),
		})
		return
	}

	// Parse to extract the first place.
	var data map[string]any
	if err := json.Unmarshal(respBody, &data); err != nil {
		c.Data(resp.StatusCode, "application/json", respBody)
		return
	}

	places, _ := data["places"].([]any)
	if len(places) == 0 {
		c.JSON(http.StatusOK, gin.H{"status": "NOT_FOUND", "result": nil})
		return
	}

	place := places[0].(map[string]any)
	// Transform to a shape the client expects.
	result := map[string]any{
		"name":                   place["displayName"],
		"formatted_address":      place["formattedAddress"],
		"formatted_phone_number": place["internationalPhoneNumber"],
		"rating":                 place["rating"],
		"reviews":                place["reviews"],
		"photos":                 place["photos"],
	}
	c.JSON(http.StatusOK, gin.H{
		"status": "OK",
		"result": result,
	})
}

// getPlacePhoto proxies a Google Places (New) Photo request.
// Returns the photo as a base64 data URI so the client can use Image.network directly.
func (h *Handler) getPlacePhoto(c *gin.Context) {
	key := h.cfg.PlacesAPIKey
	if key == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "not_configured",
			"message": "GOOGLE_PLACES_API_KEY is not set on the gateway",
		})
		return
	}

	photoName := c.Query("photo_name")
	if photoName == "" {
		// Fall back to legacy photo_reference param for compatibility.
		photoName = c.Query("photo_reference")
	}
	if photoName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "bad_request",
			"message": "photo_name query parameter is required",
		})
		return
	}

	maxWidth := c.Query("maxwidth")
	if maxWidth == "" {
		maxWidth = "400"
	}

	googleURL := fmt.Sprintf("https://places.googleapis.com/v1/%s/media?maxWidthPx=%s&key=%s", photoName, maxWidth, key)
	resp, err := http.Get(googleURL)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":   "upstream_error",
			"message": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":   "upstream_error",
			"message": err.Error(),
		})
		return
	}
	if resp.StatusCode != http.StatusOK {
		c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
		return
	}
	// Encode as base64 data URI so the client can use Image.network directly.
	b64 := base64.StdEncoding.EncodeToString(body)
	c.JSON(http.StatusOK, gin.H{
		"photo_data": "data:" + resp.Header.Get("Content-Type") + ";base64," + b64,
	})
}
