package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/karum-labs/karum-server/internal/auth"
	"github.com/karum-labs/karum-server/internal/orchestrator"
	"github.com/karum-labs/karum-server/internal/presets"
)

type prewarmResponse struct {
	Presets []prewarmPreset `json:"presets"`
}

type prewarmPreset struct {
	Preset     string `json:"preset"`
	JobID      string `json:"job_id,omitempty"`
	CampaignID string `json:"campaign_id,omitempty"`
	State      string `json:"state,omitempty"`
	Stored     bool   `json:"stored"`
	Error      string `json:"error,omitempty"`
}

// postPrewarm runs each locked preset end-to-end once and stores the completed
// run, so a judge click serves the newest ready copy without re-running the
// pipeline (PLAN.md §39). The request is authenticated; the public scout routes
// stay anonymous.
func (h *Handler) postPrewarm(c *gin.Context) {
	account, ok := auth.Current(c)
	if !ok {
		writeError(c, http.StatusUnauthorized, "unauthorized", "sign in is required")
		return
	}
	refresh := false
	if raw := strings.TrimSpace(c.Query("refresh")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(c, http.StatusBadRequest, "validation", "refresh must be true or false")
			return
		}
		refresh = value
	}

	catalog := h.catalog.All()
	if id := strings.TrimSpace(c.Query("preset")); id != "" {
		preset, found := h.catalog.Get(id)
		if !found {
			writeError(c, http.StatusBadRequest, "validation", "unknown preset")
			return
		}
		catalog = []presets.Preset{preset}
	}

	runner, err := h.runnerFor(false)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "upstream", err.Error())
		return
	}

	response := prewarmResponse{Presets: make([]prewarmPreset, 0, len(catalog))}
	failed := false
	for _, preset := range catalog {
		result := prewarmPreset{Preset: preset.ID, CampaignID: presets.CampaignID(preset.ID)}
		if !refresh {
			if stored, found := h.store.NewestReady(preset.ID, account.ID); found {
				result.JobID = stored.ID
				result.CampaignID = stored.CampaignID
				result.State = string(stored.State)
				result.Stored = true
				response.Presets = append(response.Presets, result)
				continue
			}
		}

		built, _, status, code, message := h.buildRequest(
			c.Request.Context(),
			scoutBody{PresetID: preset.ID},
			preset,
			true,
		)
		if status != 0 {
			result.Error = message
			if code != "" {
				result.Error = code + ": " + message
			}
			failed = true
			response.Presets = append(response.Presets, result)
			continue
		}
		built.AccountID = account.ID
		built.ID = h.store.NextID()

		run, runErr := runner.Run(c.Request.Context(), built)
		if runErr != nil || run == nil {
			result.Error = errString(runErr)
			failed = true
			response.Presets = append(response.Presets, result)
			continue
		}
		result.JobID = run.ID
		result.CampaignID = run.CampaignID
		result.State = string(run.State)
		result.Stored = run.State == orchestrator.StateReady || run.State == orchestrator.StateDegraded
		if !result.Stored {
			result.Error = run.ErrorMessage
			if result.Error == "" {
				result.Error = "run did not reach a stored state"
			}
			failed = true
		}
		response.Presets = append(response.Presets, result)
	}

	status := http.StatusOK
	if failed {
		status = http.StatusBadGateway
	}
	c.JSON(status, response)
}
