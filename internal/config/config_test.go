package config

import (
	"testing"
	"time"
)

func TestFromEnvUsesDefaults(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://karum:karum@db.example:5432/karum")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}

	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
	if cfg.Mode != ModeFixture {
		t.Errorf("Mode = %q, want %q", cfg.Mode, ModeFixture)
	}
	if cfg.TasteAddr != "localhost:8081" {
		t.Errorf("TasteAddr = %q, want localhost:8081", cfg.TasteAddr)
	}
	if cfg.AIAddr != "localhost:8082" {
		t.Errorf("AIAddr = %q, want localhost:8082", cfg.AIAddr)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 10s", cfg.ShutdownTimeout)
	}
	if got := cfg.Addr(); got != ":8080" {
		t.Errorf("Addr() = %q, want :8080", got)
	}
}

func TestFromEnvRejectsMissingDSN(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "")

	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil, want error for missing POSTGRES_DSN")
	}
}

func TestFromEnvRejectsInvalidMode(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://karum:karum@db.example:5432/karum")
	t.Setenv("GATEWAY_MODE", "bogus")

	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil, want error for invalid mode")
	}
}

func TestFromEnvRejectsInvalidPort(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://karum:karum@db.example:5432/karum")
	t.Setenv("GATEWAY_PORT", "0")

	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil, want error for out-of-range port")
	}
}

func TestFromEnvReadsOverrides(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://karum:karum@db.example:5432/karum")
	t.Setenv("GATEWAY_PORT", "9090")
	t.Setenv("GATEWAY_MODE", "live")
	t.Setenv("TASTE_ADDR", "taste:9091")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}

	if cfg.Port != 9090 {
		t.Errorf("Port = %d, want 9090", cfg.Port)
	}
	if cfg.Mode != ModeLive {
		t.Errorf("Mode = %q, want %q", cfg.Mode, ModeLive)
	}
	if cfg.TasteAddr != "taste:9091" {
		t.Errorf("TasteAddr = %q, want taste:9091", cfg.TasteAddr)
	}
}
