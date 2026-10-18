// Package config loads gateway configuration from the environment.
//
// Every variable listed here is documented in the infra repo's .env.example,
// along with the process that reads it.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Mode describes how the gateway is currently operating. It is surfaced on
// GET /health so the client can show a fixture/degraded banner.
type Mode string

const (
	// ModeLive means real upstream credentials are present and outbound calls
	// go to live APIs.
	ModeLive Mode = "live"
	// ModeFixture means the stack is replaying recorded payloads and makes no
	// outbound network calls.
	ModeFixture Mode = "fixture"
	// ModeDegraded means an upstream dependency is unavailable and the gateway
	// is running on its deterministic autopilot fallback.
	ModeDegraded Mode = "degraded"
)

// Config holds all gateway settings resolved from the environment.
type Config struct {
	// Port is the TCP port the HTTP server listens on.
	Port int
	// Mode is the operational mode reported by /health.
	Mode Mode
	// Version is the build version stamped into binary responses.
	Version string

	// TasteAddr is the HTTP address of service-taste, used for /health.
	TasteAddr string
	// AIAddr is the HTTP address of service-ai, used for /health.
	AIAddr string
	// TasteGRPC is the gRPC address of service-taste.
	TasteGRPC string
	// AIGRPC is the gRPC address of service-ai.
	AIGRPC string

	// RedisAddr is the Redis connection address used for caching.
	RedisAddr string
	// PostgresDSN is the Postgres connection string.
	PostgresDSN string

	// ReadTimeout bounds how long reading any request may take.
	ReadTimeout time.Duration
	// WriteTimeout bounds how long writing any response may take. Zero keeps
	// SSE streams open; per-RPC deadlines bound the upstream calls instead.
	WriteTimeout time.Duration
	// ShutdownTimeout bounds graceful shutdown.
	ShutdownTimeout time.Duration

	// PlacesAPIKey enriches doors. Empty leaves enrichment unknown.
	PlacesAPIKey string
	// RoutesAPIKey orders the field route. Empty skips routing.
	RoutesAPIKey string
	// GeocodeAPIKey resolves a city. Empty falls back to PlacesAPIKey.
	GeocodeAPIKey string

	// TasteTimeout bounds one taste RPC.
	TasteTimeout time.Duration
	// AITimeout bounds one AI RPC.
	AITimeout time.Duration
	// PlacesTimeout bounds one Places lookup batch.
	PlacesTimeout time.Duration
	// RoutesTimeout bounds one Routes call.
	RoutesTimeout time.Duration
}

// FromEnv builds a Config from environment variables. POSTGRES_DSN is required.
// Other settings fall back to local defaults when unset.
func FromEnv() (*Config, error) {
	cfg := &Config{
		Port:            getenvInt("GATEWAY_PORT", 8080),
		Mode:            Mode(getenv("GATEWAY_MODE", string(ModeFixture))),
		Version:         getenv("GATEWAY_VERSION", "dev"),
		TasteAddr:       getenv("TASTE_ADDR", "localhost:8081"),
		AIAddr:          getenv("AI_ADDR", "localhost:8082"),
		TasteGRPC:       getenv("TASTE_GRPC_ADDR", "localhost:9091"),
		AIGRPC:          getenv("AI_GRPC_ADDR", "localhost:9092"),
		RedisAddr:       getenv("REDIS_ADDR", "localhost:6379"),
		PostgresDSN:     os.Getenv("POSTGRES_DSN"),
		ReadTimeout:     getenvDuration("GATEWAY_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:    getenvDuration("GATEWAY_WRITE_TIMEOUT", 0),
		ShutdownTimeout: getenvDuration("GATEWAY_SHUTDOWN_TIMEOUT", 10*time.Second),
		PlacesAPIKey:    os.Getenv("GOOGLE_PLACES_API_KEY"),
		RoutesAPIKey:    os.Getenv("GOOGLE_ROUTES_API_KEY"),
		GeocodeAPIKey:   firstNonEmpty(os.Getenv("GOOGLE_GEOCODING_API_KEY"), os.Getenv("GOOGLE_PLACES_API_KEY")),
		TasteTimeout:    getenvDuration("GATEWAY_TASTE_TIMEOUT", 8*time.Second),
		AITimeout:       getenvDuration("GATEWAY_AI_TIMEOUT", 30*time.Second),
		PlacesTimeout:   getenvDuration("GATEWAY_PLACES_TIMEOUT", 5*time.Second),
		RoutesTimeout:   getenvDuration("GATEWAY_ROUTES_TIMEOUT", 5*time.Second),
	}

	switch cfg.Mode {
	case ModeLive, ModeFixture, ModeDegraded:
	default:
		return nil, fmt.Errorf("invalid GATEWAY_MODE %q: must be one of live, fixture, degraded", cfg.Mode)
	}

	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("invalid GATEWAY_PORT %d: must be between 1 and 65535", cfg.Port)
	}

	if cfg.PostgresDSN == "" {
		return nil, fmt.Errorf("POSTGRES_DSN is required")
	}

	return cfg, nil
}

// Addr returns the listen address for the HTTP server.
func (c *Config) Addr() string {
	return ":" + strconv.Itoa(c.Port)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// getenv returns the value of key, or fallback when unset or empty.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getenvInt parses an integer environment variable, or returns fallback.
func getenvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// getenvDuration parses a Go-duration environment variable, or returns fallback.
func getenvDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
