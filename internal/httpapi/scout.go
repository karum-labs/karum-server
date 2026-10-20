package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/karum-labs/karum-server/internal/auth"
	"github.com/karum-labs/karum-server/internal/geocode"
	"github.com/karum-labs/karum-server/internal/orchestrator"
	"github.com/karum-labs/karum-server/internal/presets"
	"github.com/karum-labs/karum-server/internal/resilience"
)

func (h *Handler) postScout(c *gin.Context) {
	if !h.limiter.Allow(c.ClientIP()) {
		writeError(c, http.StatusTooManyRequests, "rate_limited", "too many requests; retry shortly")
		return
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" {
		writeError(c, http.StatusBadRequest, "validation", "Idempotency-Key is required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		writeError(c, http.StatusBadRequest, "validation", "could not read the request body")
		return
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if prev, ok, err := h.idem.Lookup(c.Request.Context(), key); err == nil && ok {
		if prev.Hash != hash {
			writeError(c, http.StatusConflict, "conflict", "Idempotency-Key was already used with a different request")
			return
		}
		h.writeExisting(c, prev.JobID)
		return
	}

	var req scoutBody
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(c, http.StatusBadRequest, "validation", "request body is not valid JSON")
		return
	}
	preset, hasPreset := h.catalog.Get(req.PresetID)
	if req.PresetID != "" && !hasPreset {
		writeError(c, http.StatusBadRequest, "validation", "unknown preset")
		return
	}
	if err := validateScout(req, hasPreset && !req.FreeEntry); err != nil {
		writeError(c, http.StatusBadRequest, "validation", err.Error())
		return
	}
	if hasPreset && !req.FreeEntry && !req.Rerun {
		if stored, ok := h.store.NewestReady(req.PresetID); ok {
			if won, err := h.idem.Remember(c.Request.Context(), key, resilience.Idempotent{Hash: hash, JobID: stored.ID}); err == nil && !won {
				if prev, ok, err := h.idem.Lookup(c.Request.Context(), key); err == nil && ok {
					h.writeExisting(c, prev.JobID)
					return
				}
			}
			c.JSON(http.StatusOK, jobOf(stored))
			return
		}
	}

	built, candidates, status, code, message := h.buildRequest(c.Request.Context(), req, preset, hasPreset)
	if status != 0 {
		if code == "ambiguous" {
			c.JSON(status, gin.H{"error": code, "message": message, "candidates": candidateBodies(candidates)})
			return
		}
		writeError(c, status, code, message)
		return
	}
	runner, err := h.runnerFor(req.FreeEntry)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "upstream", err.Error())
		return
	}
	if account, ok := auth.Current(c); ok {
		built.AccountID = account.ID
	}
	built.ID = h.store.NextID()
	if built.CampaignID == "" {
		if hasPreset && !req.FreeEntry {
			built.CampaignID = presets.CampaignID(req.PresetID)
		} else {
			built.CampaignID = h.store.NextCampaignID()
			h.store.UpsertCampaign(orchestrator.CampaignRecord{
				ID: built.CampaignID, Mode: built.Mode, City: built.City, Point: built.Point,
				RadiusMeters: built.RadiusMeters, BrandName: built.Brand.Name, Seed: built.Seed,
				AccountID: built.AccountID,
			})
		}
	}
	if won, err := h.idem.Remember(c.Request.Context(), key, resilience.Idempotent{Hash: hash, JobID: built.ID}); err == nil && !won {
		if prev, ok, err := h.idem.Lookup(c.Request.Context(), key); err == nil && ok {
			h.writeExisting(c, prev.JobID)
			return
		}
	}

	parent := c.Request.Context()
	if h.async {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	h.cancels.put(built.ID, cancel)
	if h.async {
		go func() {
			defer h.cancels.drop(built.ID)
			defer cancel()
			_, _ = runner.Run(ctx, built)
		}()
		c.JSON(http.StatusAccepted, jobResponse{JobID: built.ID, CampaignID: built.CampaignID, State: string(orchestrator.StateRunning)})
		return
	}
	defer h.cancels.drop(built.ID)
	defer cancel()
	run, err := runner.Run(ctx, built)
	if run == nil {
		writeError(c, http.StatusBadRequest, "validation", errString(err))
		return
	}
	c.JSON(http.StatusCreated, jobOf(run))
}

func (h *Handler) buildRequest(ctx context.Context, body scoutBody, preset presets.Preset, usePreset bool) (orchestrator.Request, []geocode.Candidate, int, string, string) {
	req := orchestrator.Request{
		PresetID: body.PresetID, FreeEntry: body.FreeEntry, City: strings.TrimSpace(body.City),
		Mode: orchestrator.Mode(body.Mode), RadiusMeters: body.RadiusMeters,
		Brand:         orchestrator.BrandContext{Name: strings.TrimSpace(body.BrandName)},
		RawBrandQuery: strings.TrimSpace(body.BrandQuery), RawFallbacks: body.FallbackQueries,
		CategoryTags: body.CategoryTags, SkipCandidateIDs: body.SkipCandidateIDs,
		TraceID: newTraceID(),
	}
	for _, sku := range body.SKUs {
		req.Brand.SKUs = append(req.Brand.SKUs, orchestrator.SKU{ID: sku.ID, Name: sku.Name})
	}
	for _, entity := range body.SeedEntities {
		req.Seed = append(req.Seed, orchestrator.SeedEntity{ID: entity.ID, Type: entity.Type, Name: entity.Name})
	}
	if usePreset {
		req.Mode = orchestrator.Mode(preset.Mode)
		req.City = preset.City
		req.Brand.Name = preset.Brand
		req.Point = orchestrator.Point{Lat: preset.Lat, Lng: preset.Lng}
		req.RadiusMeters = preset.RadiusMeters
		req.HeatmapRadius = preset.HeatmapRadius
		req.CategoryTags = append([]string(nil), preset.CategoryTags...)
		req.SkipCandidateIDs = append([]string(nil), preset.SkipCandidateIDs...)
		req.Seed = nil
		for _, entity := range preset.Seed {
			req.Seed = append(req.Seed, orchestrator.SeedEntity{ID: entity.ID, Type: entity.Type, Name: entity.Name})
		}
		req.CampaignID = presets.CampaignID(preset.ID)
		req.RawBrandQuery = ""
		return req, nil, 0, "", ""
	}
	if body.Point != nil {
		req.Point = orchestrator.Point{Lat: body.Point.Lat, Lng: body.Point.Lng}
	} else {
		if h.geocoder == nil {
			return req, nil, http.StatusServiceUnavailable, "upstream", "geocoder is not configured"
		}
		result, err := h.geocoder.Geocode(ctx, req.City)
		if err != nil {
			var validation *geocode.ValidationError
			if errors.As(err, &validation) {
				return req, nil, http.StatusBadRequest, "validation", validation.Error()
			}
			return req, nil, http.StatusBadGateway, "upstream", "geocoder failed"
		}
		if len(result.Candidates) > 0 && result.Resolved == (geocode.Point{}) {
			return req, result.Candidates, http.StatusUnprocessableEntity, "ambiguous", "city query is ambiguous"
		}
		req.Point = orchestrator.Point{Lat: result.Resolved.Lat, Lng: result.Resolved.Lng}
		req.PointAttribution = result.Attribution
		req.FormattedAddress = result.FormattedAddress
		req.City = firstCity(result.FormattedAddress, req.City)
	}
	if req.RadiusMeters == 0 {
		req.RadiusMeters = 8000
	}
	if req.Mode == "" {
		req.Mode = orchestrator.ModeWholesale
	}
	if req.Mode == orchestrator.ModeFlagship && req.HeatmapRadius == 0 {
		req.HeatmapRadius = 10000
	}
	return req, nil, 0, "", ""
}

func candidateBodies(cands []geocode.Candidate) []gin.H {
	out := make([]gin.H, 0, len(cands))
	for _, cand := range cands {
		out = append(out, gin.H{
			"formatted_address": cand.FormattedAddress,
			"lat":               cand.Point.Lat,
			"lng":               cand.Point.Lng,
		})
	}
	return out
}

func (h *Handler) getScout(c *gin.Context) {
	run, ok := h.store.Get(c.Param("id"))
	if !ok {
		writeError(c, http.StatusNotFound, "not_found", "no such job")
		return
	}
	if run.State == orchestrator.StateRunning || run.State == orchestrator.StateResolving {
		c.JSON(http.StatusOK, jobOf(run))
		return
	}
	c.JSON(http.StatusOK, campaignOf(run))
}

func (h *Handler) getCampaign(c *gin.Context) {
	record, ok := h.store.Campaign(c.Param("id"))
	if !ok {
		writeError(c, http.StatusNotFound, "not_found", "no such campaign")
		return
	}
	run, ok := h.store.NewestForCampaign(record.ID)
	if !ok {
		c.JSON(http.StatusOK, gin.H{
			"campaign_id": record.ID,
			"preset_id":   record.PresetID,
			"city":        record.City,
			"mode":        record.Mode,
			"state":       "absent",
		})
		return
	}
	c.JSON(http.StatusOK, campaignOf(run))
}

func (h *Handler) postRunSheet(c *gin.Context) {
	h.renderDraft(c, false)
}

func (h *Handler) postSiteBrief(c *gin.Context) {
	h.renderDraft(c, true)
}

func (h *Handler) renderDraft(c *gin.Context, brief bool) {
	run, ok := h.store.Get(c.Param("id"))
	if !ok {
		writeError(c, http.StatusNotFound, "not_found", "no such job")
		return
	}
	if brief && run.Mode != orchestrator.ModeFlagship {
		writeError(c, http.StatusBadRequest, "validation", "the site brief is the flagship draft")
		return
	}
	if !brief && run.Mode == orchestrator.ModeFlagship {
		writeError(c, http.StatusBadRequest, "validation", "flagship downloads the site brief")
		return
	}
	doc := documentOf(run, brief)
	var (
		raw []byte
		err error
	)
	if brief {
		raw, err = h.renderer.SiteBrief(doc)
	} else {
		raw, err = h.renderer.RunSheet(doc)
	}
	if err != nil {
		writeError(c, http.StatusBadGateway, "upstream", "the pdf could not be rendered; the campaign is unchanged")
		return
	}
	name := "run-sheet.pdf"
	if brief {
		name = "site-brief.pdf"
	}
	c.Header("Content-Disposition", "attachment; filename="+name)
	c.Data(http.StatusOK, "application/pdf", raw)
}

func (h *Handler) postDoorPitch(c *gin.Context) {
	runner, err := h.runnerFor(false)
	if err != nil || runner == nil {
		writeError(c, http.StatusServiceUnavailable, "upstream", "no pipeline is configured")
		return
	}
	run, err := runner.RegenerateDoor(c.Request.Context(), c.Param("id"), c.Param("doorID"), nil)
	if err != nil {
		if errors.Is(err, orchestrator.ErrRunNotFound) || errors.Is(err, orchestrator.ErrDoorNotFound) {
			writeError(c, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeError(c, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	c.JSON(http.StatusOK, campaignOf(run))
}

func (h *Handler) postAbort(c *gin.Context) {
	if _, ok := h.store.Get(c.Param("id")); !ok {
		writeError(c, http.StatusNotFound, "not_found", "no such job")
		return
	}
	if !h.cancels.cancel(c.Param("id")) {
		writeError(c, http.StatusConflict, "conflict", "the job is not running")
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"job_id": c.Param("id"), "state": "canceling"})
}

func (h *Handler) writeExisting(c *gin.Context, id string) {
	run, ok := h.store.Get(id)
	if !ok {
		c.JSON(http.StatusOK, jobResponse{JobID: id})
		return
	}
	c.JSON(http.StatusOK, jobOf(run))
}

func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": code, "message": message})
}

func errString(err error) string {
	if err == nil {
		return "the scout could not start"
	}
	return err.Error()
}

func firstCity(formatted, fallback string) string {
	if strings.TrimSpace(formatted) != "" {
		return formatted
	}
	return fallback
}

func newTraceID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
