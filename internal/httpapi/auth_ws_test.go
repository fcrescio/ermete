package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ermete/internal/config"
	"ermete/internal/observability"
	"ermete/internal/session"
	"ermete/internal/storage"
	wrtc "ermete/internal/webrtc"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

func testAPI(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()
	logger := zap.NewNop()
	metrics := observability.NewMetrics(prometheus.NewRegistry())
	store, err := storage.NewFrameStore(cfg.DataDir, cfg.IdempotencyTTL, cfg.IdempotencyMax, metrics)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(cfg.SessionPolicy)
	webrtcSvc, err := wrtc.NewService(cfg, logger, metrics, sessions, store)
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(cfg, logger, metrics, store, sessions, webrtcSvc)
}

func TestRequirePSKMiddleware(t *testing.T) {
	api := &API{cfg: config.Config{PSK: "secret", PSKHeader: "X-Ermete-PSK", PSKAllowQuery: false}, logger: zap.NewNop()}
	h := api.requirePSK(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing psk, got %d", w.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-Ermete-PSK", "wrong")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid psk, got %d", w2.Code)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.Header.Set("X-Ermete-PSK", "secret")
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid psk, got %d", w3.Code)
	}
}

func TestWSOriginAndPSKChecks(t *testing.T) {
	cfg := config.Config{
		DataDir:             t.TempDir(),
		MaxUploadMB:         1,
		SessionPolicy:       config.SessionPolicyRejectSecond,
		UploadRatePerSec:    100,
		UploadRateBurst:     100,
		WSRatePerSec:        100,
		WSRateBurst:         100,
		PSK:                 "secret",
		PSKHeader:           "X-Ermete-PSK",
		WSAllowedOrigins:    []string{"https://allowed.example"},
		WSAllowNoOrigin:     true,
		RateLimitMaxEntries: 1000,
		RateLimitTTL:        30 * time.Minute,
		IdempotencyTTL:      10 * time.Minute,
		IdempotencyMax:      1000,
	}
	server := httptest.NewServer(testAPI(t, cfg))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/ws"

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/ws", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("X-Ermete-PSK", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for forbidden origin, got %d", resp.StatusCode)
	}

	dialer := websocket.Dialer{}
	headers := http.Header{}
	headers.Set("X-Ermete-PSK", "wrong")
	_, badResp, err := dialer.Dial(wsURL, headers)
	if err == nil {
		t.Fatal("expected ws auth failure")
	}
	if badResp == nil || badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad psk, got %+v", badResp)
	}

	headers2 := http.Header{}
	headers2.Set("X-Ermete-PSK", "secret")
	conn, okResp, err := dialer.Dial(wsURL, headers2)
	if err != nil {
		t.Fatalf("expected ws connection without origin when allowed, err=%v resp=%v", err, okResp)
	}
	_ = conn.Close()
}
