package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type SessionPolicy string

const (
	SessionPolicyRejectSecond SessionPolicy = "reject_second"
	SessionPolicyKickPrevious SessionPolicy = "kick_previous"
)

type Config struct {
	HTTPAddr            string
	DataDir             string
	MaxUploadMB         int64
	CORSAllowedOrigins  []string
	SessionPolicy       SessionPolicy
	LogLevel            string
	TLSCertFile         string
	TLSKeyFile          string
	WebRTCStunURLs      []string
	WebRTCTurnURLs      []string
	WebRTCTurnUser      string
	WebRTCTurnPass      string
	ReadHeaderTimeout   time.Duration
	WriteTimeout        time.Duration
	ReadTimeout         time.Duration
	IdleTimeout         time.Duration
	ShutdownGracePeriod time.Duration
	UploadRatePerSec    float64
	UploadRateBurst     int
	WSRatePerSec        float64
	WSRateBurst         int
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:            getEnv("HTTP_ADDR", ":8080"),
		DataDir:             getEnv("DATA_DIR", "/data"),
		LogLevel:            strings.ToLower(getEnv("LOG_LEVEL", "info")),
		TLSCertFile:         os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:          os.Getenv("TLS_KEY_FILE"),
		ReadHeaderTimeout:   10 * time.Second,
		WriteTimeout:        30 * time.Second,
		ReadTimeout:         30 * time.Second,
		IdleTimeout:         120 * time.Second,
		ShutdownGracePeriod: 15 * time.Second,
		UploadRatePerSec:    2,
		UploadRateBurst:     5,
		WSRatePerSec:        1,
		WSRateBurst:         2,
	}

	maxUploadMB, err := parseInt64Env("MAX_UPLOAD_MB", 10)
	if err != nil {
		return Config{}, err
	}
	if maxUploadMB <= 0 {
		return Config{}, fmt.Errorf("MAX_UPLOAD_MB must be > 0")
	}
	cfg.MaxUploadMB = maxUploadMB

	originsRaw := os.Getenv("CORS_ALLOWED_ORIGINS")
	if originsRaw != "" {
		cfg.CORSAllowedOrigins = splitCSV(originsRaw)
	}

	policy := SessionPolicy(getEnv("SESSION_POLICY", string(SessionPolicyRejectSecond)))
	switch policy {
	case SessionPolicyRejectSecond, SessionPolicyKickPrevious:
		cfg.SessionPolicy = policy
	default:
		return Config{}, fmt.Errorf("invalid SESSION_POLICY: %s", policy)
	}

	cfg.WebRTCStunURLs = splitCSV(os.Getenv("WEBRTC_STUN_URLS"))
	cfg.WebRTCTurnURLs = splitCSV(os.Getenv("WEBRTC_TURN_URLS"))
	cfg.WebRTCTurnUser = os.Getenv("WEBRTC_TURN_USER")
	cfg.WebRTCTurnPass = os.Getenv("WEBRTC_TURN_PASS")

	return cfg, nil
}

func (c Config) MaxUploadBytes() int64 {
	return c.MaxUploadMB * 1024 * 1024
}

func parseInt64Env(name string, defaultVal int64) (int64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return defaultVal, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return v, nil
}

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
