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
	PSK                 string
	PSKHeader           string
	AllowNoPSK          bool
	PSKAllowQuery       bool
	CORSAllowedOrigins  []string
	WSAllowedOrigins    []string
	WSAllowNoOrigin     bool
	WSAllowAnyOrigin    bool
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
	RateLimitMaxEntries int
	RateLimitTTL        time.Duration
	IdempotencyTTL      time.Duration
	IdempotencyMax      int
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
		PSKHeader:           getEnv("ERMETE_PSK_HEADER", "X-Ermete-PSK"),
		WSAllowNoOrigin:     true,
		RateLimitMaxEntries: 10000,
		RateLimitTTL:        30 * time.Minute,
		IdempotencyTTL:      10 * time.Minute,
		IdempotencyMax:      50000,
	}

	cfg.PSK = os.Getenv("ERMETE_PSK")
	cfg.AllowNoPSK = parseBoolEnv("ERMETE_ALLOW_NO_PSK", false)
	if cfg.PSK == "" && !cfg.AllowNoPSK {
		return Config{}, fmt.Errorf("ERMETE_PSK is required unless ERMETE_ALLOW_NO_PSK=true")
	}
	if strings.TrimSpace(cfg.PSKHeader) == "" {
		return Config{}, fmt.Errorf("ERMETE_PSK_HEADER cannot be empty")
	}
	cfg.PSKAllowQuery = parseBoolEnv("ERMETE_PSK_ALLOW_QUERY", false)
	cfg.WSAllowNoOrigin = parseBoolEnv("WS_ALLOW_NO_ORIGIN", true)
	cfg.WSAllowAnyOrigin = parseBoolEnv("WS_ALLOW_ANY_ORIGIN", false)

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

	cfg.WSAllowedOrigins = normalizeOriginList(splitCSV(os.Getenv("WS_ALLOWED_ORIGINS")))

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

	if v, err := parseIntEnv("RATE_LIMIT_MAX_ENTRIES", cfg.RateLimitMaxEntries); err != nil {
		return Config{}, err
	} else if v <= 0 {
		return Config{}, fmt.Errorf("RATE_LIMIT_MAX_ENTRIES must be > 0")
	} else {
		cfg.RateLimitMaxEntries = v
	}
	if v, err := parseDurationEnv("RATE_LIMIT_TTL", cfg.RateLimitTTL); err != nil {
		return Config{}, err
	} else {
		cfg.RateLimitTTL = v
	}

	if v, err := parseDurationEnv("IDEMPOTENCY_TTL", cfg.IdempotencyTTL); err != nil {
		return Config{}, err
	} else {
		cfg.IdempotencyTTL = v
	}
	if v, err := parseIntEnv("IDEMPOTENCY_MAX", cfg.IdempotencyMax); err != nil {
		return Config{}, err
	} else if v <= 0 {
		return Config{}, fmt.Errorf("IDEMPOTENCY_MAX must be > 0")
	} else {
		cfg.IdempotencyMax = v
	}

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

func parseIntEnv(name string, defaultVal int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return defaultVal, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return v, nil
}

func parseDurationEnv(name string, defaultVal time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return defaultVal, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s must be > 0", name)
	}
	return v, nil
}

func parseBoolEnv(name string, defaultVal bool) bool {
	raw := os.Getenv(name)
	if raw == "" {
		return defaultVal
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return defaultVal
	}
	return v
}

func normalizeOriginList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, origin := range in {
		norm := strings.TrimSpace(origin)
		norm = strings.TrimSuffix(norm, "/")
		if norm != "" {
			out = append(out, norm)
		}
	}
	return out
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
