package config

import (
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("DATA_DIR", "")
	t.Setenv("MAX_UPLOAD_MB", "")
	t.Setenv("SESSION_POLICY", "")
	t.Setenv("ERMETE_ALLOW_NO_PSK", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.DataDir != "/data" || cfg.MaxUploadMB != 10 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.PSKHeader != "X-Ermete-PSK" {
		t.Fatalf("unexpected PSK header default: %s", cfg.PSKHeader)
	}
}

func TestInvalidSessionPolicy(t *testing.T) {
	t.Setenv("SESSION_POLICY", "bad")
	t.Setenv("ERMETE_ALLOW_NO_PSK", "true")
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadCSV(t *testing.T) {
	t.Setenv("WEBRTC_STUN_URLS", "stun:1, stun:2")
	t.Setenv("WS_ALLOWED_ORIGINS", "https://a.example/, https://b.example")
	t.Setenv("ERMETE_ALLOW_NO_PSK", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.WebRTCStunURLs) != 2 {
		t.Fatalf("expected 2 stun urls, got %d", len(cfg.WebRTCStunURLs))
	}
	if len(cfg.WSAllowedOrigins) != 2 || cfg.WSAllowedOrigins[0] != "https://a.example" {
		t.Fatalf("unexpected ws allowed origins: %#v", cfg.WSAllowedOrigins)
	}
}

func TestPSKRequiredUnlessAllowed(t *testing.T) {
	t.Setenv("ERMETE_PSK", "")
	t.Setenv("ERMETE_ALLOW_NO_PSK", "false")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for missing psk")
	}

	t.Setenv("ERMETE_ALLOW_NO_PSK", "true")
	if _, err := Load(); err != nil {
		t.Fatalf("unexpected error with allow-no-psk: %v", err)
	}
}
