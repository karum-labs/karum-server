package orchestrator

import (
	"context"
	"errors"
)

// Shared error codes from karum.common.v1.ErrorCode. The gateway maps these
// onto the run state; a typed upstream failure degrades the run instead of
// dropping the taste ranking (PLAN.md §15, §18).
const (
	// CodeUnspecified is a success.
	CodeUnspecified = "ERROR_CODE_UNSPECIFIED"
	// CodeUpstream means Vertex or Qloo failed.
	CodeUpstream = "ERROR_CODE_UPSTREAM"
	// CodeValidation means the request never reached an upstream.
	CodeValidation = "ERROR_CODE_VALIDATION"
	// CodeEmpty means the call succeeded with no rows.
	CodeEmpty = "ERROR_CODE_EMPTY"
	// CodeUnresolved means a seed resolved to nothing.
	CodeUnresolved = "ERROR_CODE_UNRESOLVED"
	// CodeRateLimited means an upstream or the gateway refused for rate.
	CodeRateLimited = "ERROR_CODE_RATE_LIMITED"
)

// SeedResolver resolves a raw brand query. Confirmed seeds never call it.
type SeedResolver interface {
	ResolveBrandSeed(ctx context.Context, req SeedQuery) (SeedResolution, error)
}

// HeatmapClient fetches the flagship density layer.
type HeatmapClient interface {
	GetLocalityHeatmap(ctx context.Context, req HeatmapQuery) (HeatmapView, error)
}

// CompareClient runs the conquest comparison for one incumbent door.
type CompareClient interface {
	CompareAudiences(ctx context.Context, req CompareQuery) (CompareChart, error)
}

// UpstreamError is the typed failure service clients return. The runner
// degrades on it; the HTTP layer will map it onto a JSON error in §19.
type UpstreamError struct {
	Code    string
	Message string
	Cause   error
}

// Error implements error.
func (e *UpstreamError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// Unwrap exposes the transport cause.
func (e *UpstreamError) Unwrap() error { return e.Cause }

// CodeOf returns the proto error code an error maps onto.
func CodeOf(err error) string {
	var typed *UpstreamError
	if errors.As(err, &typed) && typed.Code != "" {
		return typed.Code
	}
	return CodeUpstream
}

// TasteClient is the service-taste transport. The gRPC implementation lands in
// §19; tests use in-process fakes.
type TasteClient interface {
	ScoutDoors(ctx context.Context, req ScoutRequest) (ScoutResult, error)
}

// AgentClient is the service-ai transport.
type AgentClient interface {
	AuditMenu(ctx context.Context, req AuditRequest) (AuditResult, error)
	DraftDoorPitch(ctx context.Context, req DraftRequest) (DraftResult, error)
	LlmOnlyBaseline(ctx context.Context, req BaselineRequest) (BaselineResult, error)
}
