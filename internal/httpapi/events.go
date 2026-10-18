package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/karum-labs/karum-server/internal/orchestrator"
)

func (h *Handler) getEvents(c *gin.Context) {
	id := c.Param("id")
	run, ok := h.store.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "not_found", "no such job")
		return
	}
	cursor := int64(0)
	if raw := c.Query("cursor"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeError(c, http.StatusBadRequest, "validation", "cursor must be a non-negative integer")
			return
		}
		cursor = n
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	sent := h.writeEvents(c, run, cursor)
	if terminal(run.State) {
		c.Status(http.StatusOK)
		return
	}
	ch, unsub := h.store.Subscribe(id)
	defer unsub()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprintf(c.Writer, ": keepalive\n\n")
			c.Writer.Flush()
		case <-ch:
			run, ok = h.store.Get(id)
			if !ok {
				return
			}
			sent = h.writeEvents(c, run, sent)
			if terminal(run.State) {
				return
			}
		}
	}
}

func (h *Handler) writeEvents(c *gin.Context, run *orchestrator.Run, after int64) int64 {
	last := after
	for _, event := range run.Trace {
		if event.Sequence <= after {
			continue
		}
		raw, err := json.Marshal(traceBody{
			Sequence: event.Sequence, Cursor: event.Cursor, Stage: string(event.Stage),
			ElapsedMS: event.ElapsedMS, CacheHit: event.CacheHit, Detail: event.Detail,
		})
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(c.Writer, "id: %s\nevent: trace\ndata: %s\n\n", event.Cursor, raw)
		last = event.Sequence
	}
	c.Writer.Flush()
	return last
}

func terminal(state orchestrator.JobState) bool {
	switch state {
	case orchestrator.StateReady, orchestrator.StateDegraded, orchestrator.StateFailed:
		return true
	default:
		return false
	}
}
