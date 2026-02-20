package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"ermete/internal/config"
	"ermete/internal/observability"
	"ermete/internal/session"
	"ermete/internal/storage"
	wrtc "ermete/internal/webrtc"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

type API struct {
	cfg      config.Config
	logger   *zap.Logger
	metrics  *observability.Metrics
	store    *storage.FrameStore
	sessions *session.Manager
	webrtc   *wrtc.Service
	started  time.Time
	limits   *Limiter
}

func NewRouter(cfg config.Config, logger *zap.Logger, metrics *observability.Metrics, store *storage.FrameStore, sessions *session.Manager, webrtc *wrtc.Service) http.Handler {
	a := &API{cfg: cfg, logger: logger, metrics: metrics, store: store, sessions: sessions, webrtc: webrtc, started: time.Now().UTC(), limits: NewLimiter(cfg.RateLimitTTL, cfg.RateLimitMaxEntries, metrics, logger)}
	r := chi.NewRouter()
	r.Use(chimw.RequestID, chimw.RealIP, chimw.Recoverer, a.requestLogger)
	if len(cfg.CORSAllowedOrigins) > 0 {
		r.Use(cors.Handler(cors.Options{AllowedOrigins: cfg.CORSAllowedOrigins, AllowedMethods: []string{"GET", "POST", "OPTIONS"}, AllowedHeaders: []string{"*"}}))
	}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/readyz", a.handleReady)
	r.Handle("/metrics", promhttp.Handler())

	r.Group(func(r chi.Router) {
		r.Use(a.rateLimitMiddleware(cfg.UploadRatePerSec, cfg.UploadRateBurst), a.requirePSK)
		r.Post("/v1/frames", a.handleFrameUpload)
	})
	r.Group(func(r chi.Router) {
		r.Use(a.rateLimitMiddleware(cfg.WSRatePerSec, cfg.WSRateBurst), a.requirePSK)
		r.Get("/v1/ws", a.handleWS)
	})
	return r
}

func (a *API) handleReady(w http.ResponseWriter, _ *http.Request) {
	if !a.store.IsReady() {
		http.Error(w, "storage not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

func (a *API) handleWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		origin := normalizeOrigin(r.Header.Get("Origin"))
		if origin == "" {
			return a.cfg.WSAllowNoOrigin
		}
		if a.cfg.WSAllowAnyOrigin {
			return true
		}
		if len(a.cfg.WSAllowedOrigins) == 0 {
			return false
		}
		for _, allowed := range a.cfg.WSAllowedOrigins {
			if origin == allowed {
				return true
			}
		}
		return false
	}}
	if !upgrader.CheckOrigin(r) {
		a.metrics.WSRejectTotal.Inc()
		a.logger.Warn("websocket origin rejected", zap.String("ip", clientIP(r)), zap.String("path", r.URL.Path), zap.String("origin", normalizeOrigin(r.Header.Get("Origin"))))
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden origin"})
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, "websocket upgrade failed", http.StatusBadRequest)
		return
	}
	a.webrtc.HandleWS(r.Context(), conn)
}

func (a *API) handleFrameUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	maxBytes := a.cfg.MaxUploadBytes()
	contentType := r.Header.Get("Content-Type")
	frameID := r.Header.Get("X-Frame-Id")
	timestamp := r.Header.Get("X-Timestamp")
	idem := r.Header.Get("X-Idempotency-Key")

	var payload []byte
	var err error
	if strings.HasPrefix(contentType, "multipart/form-data") {
		payload, contentType, err = readMultipartPayload(r, maxBytes)
	} else {
		payload, err = storage.ReadAllLimited(r.Body, maxBytes)
	}
	if err != nil {
		a.metrics.FrameUploadErrors.Inc()
		if strings.Contains(strings.ToLower(err.Error()), "too large") {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "payload too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid payload: %v", err)})
		return
	}

	meta, err := a.store.SaveFrame(frameID, timestamp, idem, contentType, payload)
	if err != nil {
		a.metrics.FrameUploadErrors.Inc()
		http.Error(w, "failed to save frame", http.StatusInternalServerError)
		return
	}
	a.metrics.FramesUploadedTotal.Inc()
	a.metrics.FrameUploadBytesTotal.Add(float64(len(payload)))
	a.sessions.Touch()

	resp := map[string]any{"status": "ok", "duplicate": meta.Duplicate, "frame": meta, "request_id": chimw.GetReqID(ctx)}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) requirePSK(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get(a.cfg.PSKHeader)
		if provided == "" && a.cfg.PSKAllowQuery {
			provided = r.URL.Query().Get("psk")
		}
		if provided == "" {
			a.logger.Warn("auth rejected", zap.String("ip", clientIP(r)), zap.String("path", r.URL.Path), zap.String("reason", "missing psk"))
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(a.cfg.PSK)) != 1 {
			a.logger.Warn("auth rejected", zap.String("ip", clientIP(r)), zap.String("path", r.URL.Path), zap.String("reason", "invalid psk"))
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func readMultipartPayload(r *http.Request, maxBytes int64) ([]byte, string, error) {
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		return nil, "", err
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	payload, err := storage.ReadAllLimited(file, maxBytes)
	if err != nil {
		return nil, "", err
	}
	return payload, detectMultipartContentType(header), nil
}

func detectMultipartContentType(h *multipart.FileHeader) string {
	if h.Header.Get("Content-Type") != "" {
		return h.Header.Get("Content-Type")
	}
	return "application/octet-stream"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *API) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		a.logger.Info("http request", zap.String("method", r.Method), zap.String("path", r.URL.Path), zap.Int("status", ww.Status()), zap.Duration("duration", time.Since(start)), zap.String("request_id", chimw.GetReqID(r.Context())))
	})
}

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type Limiter struct {
	mu         sync.Mutex
	limiters   map[string]*limiterEntry
	ttl        time.Duration
	maxEntries int
	metrics    *observability.Metrics
	logger     *zap.Logger
}

func NewLimiter(ttl time.Duration, maxEntries int, metrics *observability.Metrics, logger *zap.Logger) *Limiter {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = 10000
	}
	l := &Limiter{limiters: map[string]*limiterEntry{}, ttl: ttl, maxEntries: maxEntries, metrics: metrics, logger: logger}
	l.updateMetricsLocked()
	go l.cleanupLoop()
	return l
}

func (a *API) rateLimitMiddleware(rps float64, burst int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if !a.limits.allow(ip, rps, burst) {
				writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (l *Limiter) allow(ip string, rps float64, burst int) bool {
	now := time.Now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.limiters[ip]
	if !ok {
		if len(l.limiters) >= l.maxEntries {
			if l.metrics != nil {
				l.metrics.RateLimiterEvictionsTotal.Inc()
			}
			if l.logger != nil {
				l.logger.Warn("rate limiter map full, rejecting new IP", zap.String("ip", ip), zap.Int("entries", len(l.limiters)))
			}
			l.updateMetricsLocked()
			return false
		}
		entry = &limiterEntry{limiter: rate.NewLimiter(rate.Limit(rps), burst), lastSeen: now}
		l.limiters[ip] = entry
	} else {
		entry.lastSeen = now
	}
	entry.lastSeen = now
	l.updateMetricsLocked()
	return entry.limiter.Allow()
}

func (l *Limiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.cleanup(time.Now().UTC())
	}
}

func (l *Limiter) cleanup(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, entry := range l.limiters {
		if now.Sub(entry.lastSeen) > l.ttl {
			delete(l.limiters, ip)
			if l.metrics != nil {
				l.metrics.RateLimiterEvictionsTotal.Inc()
			}
		}
	}
	l.updateMetricsLocked()
}

func (l *Limiter) updateMetricsLocked() {
	if l.metrics != nil {
		l.metrics.RateLimiterEntries.Set(float64(len(l.limiters)))
	}
}

func normalizeOrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	origin = strings.TrimSuffix(origin, "/")
	return origin
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func DrainBody(ctx context.Context, rc io.ReadCloser) {
	defer rc.Close()
	_, _ = io.Copy(io.Discard, rc)
	select {
	case <-ctx.Done():
	default:
	}
}
