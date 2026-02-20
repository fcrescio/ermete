package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ermete/internal/config"
	"ermete/internal/observability"
	"ermete/internal/session"
	"ermete/internal/storage"
	wrtc "ermete/internal/webrtc"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

func TestUploadLimitAndSafeName(t *testing.T) {
	cfg := config.Config{MaxUploadMB: 1, DataDir: t.TempDir(), SessionPolicy: config.SessionPolicyRejectSecond, UploadRatePerSec: 100, UploadRateBurst: 100, WSRatePerSec: 100, WSRateBurst: 100, PSK: "secret", PSKHeader: "X-Ermete-PSK", WSAllowNoOrigin: true, RateLimitMaxEntries: 10000, RateLimitTTL: 30 * time.Minute}
	logger := zap.NewNop()
	metrics := observability.NewMetrics(prometheus.NewRegistry())
	store, _ := storage.NewFrameStore(cfg.DataDir, 10*time.Minute, 100, metrics)
	sessions := session.NewManager(cfg.SessionPolicy)
	webrtcSvc, _ := wrtc.NewService(cfg, logger, metrics, sessions, store)
	h := NewRouter(cfg, logger, metrics, store, sessions, webrtcSvc)

	big := bytes.Repeat([]byte("a"), int(cfg.MaxUploadBytes()+1))
	req := httptest.NewRequest(http.MethodPost, "/v1/frames", bytes.NewReader(big))
	req.Header.Set("Content-Type", "image/jpeg")
	req.Header.Set("X-Ermete-PSK", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for large payload, got %d", w.Code)
	}

	small := []byte("ok")
	req2 := httptest.NewRequest(http.MethodPost, "/v1/frames", bytes.NewReader(small))
	req2.Header.Set("Content-Type", "image/png")
	req2.Header.Set("X-Frame-Id", "../bad:id")
	req2.Header.Set("X-Ermete-PSK", "secret")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		body, _ := io.ReadAll(w2.Body)
		t.Fatalf("expected 200, got %d body=%s", w2.Code, string(body))
	}
	last, _ := store.LastMeta()
	if last.FileName == "" || last.FileName[:1] == "/" {
		t.Fatalf("unexpected filename: %s", last.FileName)
	}
}
