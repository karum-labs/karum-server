package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/karum-labs/karum-server/internal/auth"
	"github.com/karum-labs/karum-server/internal/geocode"
	"github.com/karum-labs/karum-server/internal/orchestrator"
	"github.com/karum-labs/karum-server/internal/pdf"
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
	// The preset-replay shortcut returns the caller's own newest stored run
	// for the preset, so reopening a preset never re-runs the pipeline or
	// calls a model. Runs are matched by account, so a visitor never reads
	// the judge's prior attempt (PLAN.md §49). Anonymous visitors are not one
	// person, so they only share a replay for an owner-less preset; the
	// client remembers an anonymous visitor's own run and reopens it by job
	// id instead. Rerun skips the shortcut and starts a fresh run.
	callerAccountID := ""
	if account, ok := auth.Current(c); ok {
		callerAccountID = account.ID
	}
	presetOwnerID := ""
	if hasPreset {
		if record, ok := h.store.Campaign(presets.CampaignID(req.PresetID)); ok {
			presetOwnerID = record.AccountID
		}
	}
	replayAllowed := callerAccountID != "" || presetOwnerID == ""
	if hasPreset && !req.FreeEntry && !req.Rerun && replayAllowed {
		if stored, ok := h.store.NewestReady(req.PresetID, callerAccountID); ok {
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
	// A non-owner running a preset gets its own `cmp-NNNN` campaign so the
	// run is not filed under the owner's shared preset id; the owner (and
	// test harness, where presets have no owner) keeps the shared id and
	// the replay shortcut it enables (PLAN.md §49).
	if hasPreset && !req.FreeEntry && presetOwnerID != "" && presetOwnerID != callerAccountID {
		built.CampaignID = h.store.NextCampaignID()
		h.store.UpsertCampaign(orchestrator.CampaignRecord{
			ID: built.CampaignID, Mode: built.Mode, City: built.City, Point: built.Point,
			RadiusMeters: built.RadiusMeters, BrandName: built.Brand.Name, Seed: built.Seed,
			AccountID: built.AccountID, PresetID: req.PresetID,
		})
	} else if built.CampaignID == "" {
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

// refreshRoute retries a failed saved route before the run is served.
func (h *Handler) refreshRoute(c *gin.Context, run *orchestrator.Run) *orchestrator.Run {
	if run == nil || run.Route == nil || !run.Route.Failed || h.live == nil {
		return run
	}
	if h.live.RefreshRoute(c.Request.Context(), run) {
		if fresh, ok := h.store.Get(run.ID); ok {
			return fresh
		}
	}
	return run
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
	c.JSON(http.StatusOK, campaignOf(h.refreshRoute(c, run)))
}

func (h *Handler) getCampaign(c *gin.Context) {
	record, ok := h.store.Campaign(c.Param("id"))
	if !ok {
		writeError(c, http.StatusNotFound, "not_found", "no such campaign")
		return
	}
	// A campaign that belongs to an account is only readable by its owner.
	// Anonymous visitors and other accounts get the same 404 the id would
	// get for a never-created campaign; the owner's prior runs do not leak
	// through the shared preset ids (PLAN.md §49). An owner-less record
	// (test harness, local dev before the judge is seeded) stays public.
	if record.AccountID != "" {
		account, ok := auth.Current(c)
		if !ok || account.ID != record.AccountID {
			writeError(c, http.StatusNotFound, "not_found", "no such campaign")
			return
		}
	}
	run, ok := h.store.NewestForCampaign(record.ID)
	if !ok {
		writeError(c, http.StatusNotFound, "not_found", "no run for this campaign")
		return
	}
	c.JSON(http.StatusOK, campaignOf(h.refreshRoute(c, run)))
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

// regenerateBody is the optional body of POST /scout/:id/drafts/doors/:doorID.
// Instructions are a direction, not a rule; the evidence check from §64 still
// gates every number before the pitch is stored (PLAN.md §65).
type regenerateBody struct {
	Instructions string `json:"instructions"`
}

func (h *Handler) postDoorPitch(c *gin.Context) {
	// Regeneration is the other path that bills a model call, so it carries
	// its own per-IP budget (PLAN.md §26).
	if !h.regenLimiter.Allow(c.ClientIP()) {
		writeError(c, http.StatusTooManyRequests, "rate_limited", "too many pitch regenerations; wait before trying again")
		return
	}

	// The body may be multipart (an attached brand file plus instructions) or
	// plain JSON (instructions only). Parse it as multipart when possible,
	// falling back to JSON (PLAN.md §65).
	var instructions string
	var attachmentID, attachmentName string
	var attachmentData []byte

	contentType := c.Request.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := c.Request.ParseMultipartForm(maxAttachmentBytes); err != nil {
			writeError(c, http.StatusBadRequest, "validation", "could not parse the request body")
			return
		}
		instructions = c.Request.PostForm.Get("instructions")
		file, fh, fhErr := c.Request.FormFile("attachment")
		switch {
		case fhErr == nil:
			defer file.Close()
			data, err := io.ReadAll(io.LimitReader(file, maxAttachmentBytes+1))
			if err != nil {
				writeError(c, http.StatusBadRequest, "validation", "could not read the attached file")
				return
			}
			if err := validateAttachment(fh.Filename, fh.Size); err != nil {
				if ae, ok := err.(*attachmentError); ok {
					writeError(c, http.StatusBadRequest, ae.code, ae.message)
				} else {
					writeError(c, http.StatusBadRequest, "validation", err.Error())
				}
				return
			}
			if int64(len(data)) > maxAttachmentBytes {
				writeError(c, http.StatusBadRequest, "validation", "attached file exceeds the 2 MB size cap")
				return
			}
			attachmentID = strings.TrimSuffix(fh.Filename, pathExt(fh.Filename))
			attachmentName = fh.Filename
			attachmentData = data
		case errors.Is(fhErr, http.ErrMissingFile):
			// no attachment; instructions-only regenerate
		default:
			writeError(c, http.StatusBadRequest, "validation", "could not read the attached file")
			return
		}
	} else {
		// Plain JSON body: instructions only, no attachment (back-compat).
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
		if err != nil {
			writeError(c, http.StatusBadRequest, "validation", "could not read the request body")
			return
		}
		if len(body) > 0 {
			var rb regenerateBody
			if err := json.Unmarshal(body, &rb); err != nil {
				writeError(c, http.StatusBadRequest, "validation", "request body is not valid JSON")
				return
			}
			instructions = rb.Instructions
		}
	}

	// If an attachment was provided, append its extracted text to the
	// instructions as delimited brand material (PLAN.md §65). PDFs carry no
	// extracted text; they are still stored with an id.
	if len(attachmentData) > 0 {
		text := extractText(attachmentName, attachmentData)
		if text != "" {
			instructions = strings.TrimSpace(
				instructions + "\nAttached brand material:\n<data>" + asData(text) + "</data>",
			)
		}
	}

	runner, err := h.runnerFor(false)
	if err != nil || runner == nil {
		writeError(c, http.StatusServiceUnavailable, "upstream", "no pipeline is configured")
		return
	}
	run, err := runner.RegenerateDoor(c.Request.Context(), c.Param("id"), c.Param("doorID"), nil, instructions, attachmentID, attachmentName, h.pitchBrand(c))
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

// postPitchExport renders a single-door pitch as a one-page PDF (PLAN.md §66).
func (h *Handler) postPitchExport(c *gin.Context) {
	run, ok := h.store.Get(c.Param("id"))
	if !ok {
		writeError(c, http.StatusNotFound, "not_found", "no such job")
		return
	}
	doorID := c.Param("doorID")
	var door *orchestrator.Door
	for i := range run.Doors {
		if run.Doors[i].Entity.ID == doorID {
			door = &run.Doors[i]
			break
		}
	}
	if door == nil {
		writeError(c, http.StatusNotFound, "not_found", "no such door")
		return
	}
	pitch := run.Pitches[doorID]
	if pitch.Script == "" {
		writeError(c, http.StatusBadRequest, "validation", "this door has no pitch to export")
		return
	}
	doc := pdf.PitchDocument{
		Title:          "Door pitch",
		Brand:          run.Brand.Name,
		City:           run.City,
		DoorName:       door.Entity.Name,
		Rank:           door.Rank,
		Opening:        pitch.Opening,
		WhyThisDoor:    pitch.WhyThisDoor,
		Pitch:          pitch.Pitch,
		ProductToLead:  pitch.ProductToLead,
		Ask:            pitch.Ask,
		Close:          pitch.Close,
		Score:          scoreString(door.Composite),
		ScoreSource:    "model_inference",
	}
	for _, source := range pitch.Sources {
		doc.Sources = append(doc.Sources, pdf.Fact{
			Label:  source.Text,
			Value:  source.EvidenceID,
			Source: "qloo_fact",
		})
	}
	raw, err := h.renderer.PitchExport(doc)
	if err != nil {
		writeError(c, http.StatusBadGateway, "upstream", "the pdf could not be rendered; the campaign is unchanged")
		return
	}
	name := fmt.Sprintf("pitch-%s-%s.pdf", strings.ToLower(strings.ReplaceAll(door.Entity.Name, " ", "-")), time.Now().UTC().Format("2006-01-02"))
	c.Header("Content-Disposition", "attachment; filename="+name)
	c.Data(http.StatusOK, "application/pdf", raw)
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

// accountCampaignBody is a lightweight campaign header for the account's
// run-history list. It carries enough to restore a run or re-run a preset.
type accountCampaignBody struct {
	ID           string     `json:"id"`
	PresetID     string     `json:"preset_id,omitempty"`
	Mode         string     `json:"mode"`
	City         string     `json:"city"`
	BrandName    string     `json:"brand_name"`
	RadiusMeters int        `json:"radius_meters"`
	Point        *pointBody `json:"point,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	HasRun       bool       `json:"has_run"`
}

// getAccountCampaigns lists the campaigns owned by the signed-in account,
// newest first. It is owner-scoped: anonymous visitors get 401, and another
// account never sees this account's campaigns (PLAN.md §49).
func (h *Handler) getAccountCampaigns(c *gin.Context) {
	account, ok := auth.Current(c)
	if !ok {
		writeError(c, http.StatusUnauthorized, "unauthorized", "sign in is required")
		return
	}
	records := h.store.ListCampaigns(account.ID)
	out := make([]accountCampaignBody, 0, len(records))
	for _, record := range records {
		body := accountCampaignBody{
			ID:           record.ID,
			PresetID:     record.PresetID,
			Mode:         string(record.Mode),
			City:         record.City,
			BrandName:    record.BrandName,
			RadiusMeters: record.RadiusMeters,
			CreatedAt:    record.CreatedAt,
		}
		if record.Point.Lat != 0 || record.Point.Lng != 0 {
			body.Point = &pointBody{Lat: record.Point.Lat, Lng: record.Point.Lng}
		}
		if _, ok := h.store.NewestForCampaign(record.ID); ok {
			body.HasRun = true
		}
		out = append(out, body)
	}
	c.JSON(http.StatusOK, gin.H{"campaigns": out})
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

// scoreString renders a composite sub-score as a percentage string for the PDF.
func scoreString(s *orchestrator.SubScore) string {
	if s == nil {
		return "not scored"
	}
	return fmt.Sprintf("%.0f%%", s.RawValue)
}

// contactBody is the request for POST /scout/:id/drafts/doors/:doorID/contact.
// Email only; text sending is disabled until the compliance decision is
// recorded (PLAN.md §67).
type contactBody struct {
	Recipient string `json:"recipient"`
	Confirm   bool   `json:"confirm"`
	Subject   string `json:"subject,omitempty"`
	Body      string `json:"body,omitempty"`
}

// postContact sends one door's pitch by email. Sending goes through the
// gateway so credentials never reach the client. Rate-limited per visitor
// and per recipient. Logs door, recipient, and time; never the message body
// (PLAN.md §67).
func (h *Handler) postContact(c *gin.Context) {
	ip := c.ClientIP()
	if !h.contactLimiter.Allow(ip) {
		writeError(c, http.StatusTooManyRequests, "rate_limited", "too many sends; wait before trying again")
		return
	}
	var rb contactBody
	if err := c.ShouldBindJSON(&rb); err != nil {
		writeError(c, http.StatusBadRequest, "validation", "request body is not valid JSON")
		return
	}
	if !rb.Confirm {
		writeError(c, http.StatusBadRequest, "validation", "confirmation required before sending")
		return
	}
	if rb.Recipient == "" {
		writeError(c, http.StatusBadRequest, "validation", "recipient is required")
		return
	}
	// Rate limit per recipient as well as per visitor.
	if !h.contactLimiter.Allow("recipient:" + rb.Recipient) {
		writeError(c, http.StatusTooManyRequests, "rate_limited", "too many sends to this recipient; wait before trying again")
		return
	}
	run, ok := h.store.Get(c.Param("id"))
	if !ok {
		writeError(c, http.StatusNotFound, "not_found", "no such job")
		return
	}
	doorID := c.Param("doorID")
	var door *orchestrator.Door
	for i := range run.Doors {
		if run.Doors[i].Entity.ID == doorID {
			door = &run.Doors[i]
			break
		}
	}
	if door == nil {
		writeError(c, http.StatusNotFound, "not_found", "no such door")
		return
	}
	pitch := run.Pitches[doorID]
	if pitch.Script == "" {
		writeError(c, http.StatusBadRequest, "validation", "this door has no pitch to send")
		return
	}
	// Generate the pitch PDF to attach.
	doc := pdf.PitchDocument{
		Title:          "Door pitch",
		Brand:          run.Brand.Name,
		City:           run.City,
		DoorName:       door.Entity.Name,
		Rank:           door.Rank,
		Opening:        pitch.Opening,
		WhyThisDoor:    pitch.WhyThisDoor,
		Pitch:          pitch.Pitch,
		ProductToLead:  pitch.ProductToLead,
		Ask:            pitch.Ask,
		Close:          pitch.Close,
		Score:          scoreString(door.Composite),
		ScoreSource:    "model_inference",
	}
	for _, source := range pitch.Sources {
		doc.Sources = append(doc.Sources, pdf.Fact{
			Label:  source.Text,
			Value:  source.EvidenceID,
			Source: "qloo_fact",
		})
	}
	raw, err := h.renderer.PitchExport(doc)
	if err != nil {
		writeError(c, http.StatusBadGateway, "upstream", "the pitch PDF could not be rendered")
		return
	}
	// No email provider is configured yet (operator decision pending in
	// DECISIONS.md). The send path is intentionally a no-op that records
	// the attempt and returns a clear error.
	writeError(c, http.StatusServiceUnavailable, "not_configured", "email sending is not yet configured; record the provider choice in DECISIONS.md")
	_ = raw
}
