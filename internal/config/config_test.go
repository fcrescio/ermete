package config

import (
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("DATA_DIR", "")
	t.Setenv("MAX_UPLOAD_MB", "")
	t.Setenv("SESSION_POLICY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.DataDir != "/data" || cfg.MaxUploadMB != 10 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestInvalidSessionPolicy(t *testing.T) {
	t.Setenv("SESSION_POLICY", "bad")
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadCSV(t *testing.T) {
	t.Setenv("WEBRTC_STUN_URLS", "stun:1, stun:2")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.WebRTCStunURLs) != 2 {
		t.Fatalf("expected 2 stun urls, got %d", len(cfg.WebRTCStunURLs))
	}
	_ = os.Unsetenv("WEBRTC_STUN_URLS")
}
