package observability

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	FramesUploadedTotal       prometheus.Counter
	FrameUploadBytesTotal     prometheus.Counter
	FrameUploadErrors         prometheus.Counter
	WSConnectionsTotal        prometheus.Counter
	WSRejectTotal             prometheus.Counter
	WebRTCPacketsIn           prometheus.Counter
	WebRTCPacketsOut          prometheus.Counter
	RateLimiterEntries        prometheus.Gauge
	RateLimiterEvictionsTotal prometheus.Counter
	IdempotencyEntries        prometheus.Gauge
	IdempotencyEvictionsTotal prometheus.Counter
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		FramesUploadedTotal: promautoCounter(reg, "ermete_frames_uploaded_total", "Number of successfully uploaded frames"),
		FrameUploadBytesTotal: promautoCounter(reg,
			"ermete_frame_upload_bytes_total", "Total bytes received from frame uploads"),
		FrameUploadErrors:         promautoCounter(reg, "ermete_frame_upload_errors_total", "Number of upload errors"),
		WSConnectionsTotal:        promautoCounter(reg, "ermete_ws_connections_total", "Total WebSocket connections"),
		WSRejectTotal:             promautoCounter(reg, "ermete_ws_rejections_total", "Rejected WebSocket connections"),
		WebRTCPacketsIn:           promautoCounter(reg, "ermete_webrtc_rtp_in_total", "Inbound RTP packets"),
		WebRTCPacketsOut:          promautoCounter(reg, "ermete_webrtc_rtp_out_total", "Outbound RTP packets"),
		RateLimiterEntries:        promautoGauge(reg, "ermete_rate_limiter_entries", "Current number of IP entries in the in-app rate limiter"),
		RateLimiterEvictionsTotal: promautoCounter(reg, "ermete_rate_limiter_evictions_total", "Evicted in-app rate limiter entries"),
		IdempotencyEntries:        promautoGauge(reg, "ermete_idempotency_entries", "Current idempotency key entries in memory"),
		IdempotencyEvictionsTotal: promautoCounter(reg, "ermete_idempotency_evictions_total", "Evicted idempotency keys from in-memory store"),
	}
	return m
}

func promautoCounter(reg prometheus.Registerer, name, help string) prometheus.Counter {
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
	reg.MustRegister(counter)
	return counter
}

func promautoGauge(reg prometheus.Registerer, name, help string) prometheus.Gauge {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	reg.MustRegister(gauge)
	return gauge
}
