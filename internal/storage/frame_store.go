package storage

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"ermete/internal/observability"
)

var safeToken = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

type FrameMeta struct {
	FrameID        string    `json:"frame_id"`
	Timestamp      string    `json:"timestamp,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	FileName       string    `json:"file_name"`
	Path           string    `json:"path"`
	Size           int64     `json:"size"`
	ContentType    string    `json:"content_type"`
	SHA256         string    `json:"sha256"`
	ReceivedAt     time.Time `json:"received_at"`
	Duplicate      bool      `json:"duplicate"`
}

type idemEntry struct {
	frameMeta FrameMeta
	seenAt    time.Time
	elem      *list.Element
}

type FrameStore struct {
	root      string
	framesDir string
	mu        sync.Mutex

	byIdempotency map[string]*idemEntry
	idemOrder     *list.List
	idemTTL       time.Duration
	idemMax       int

	last  FrameMeta
	count uint64

	metrics *observability.Metrics
}

func NewFrameStore(dataDir string, idemTTL time.Duration, idemMax int, metrics *observability.Metrics) (*FrameStore, error) {
	if idemTTL <= 0 {
		idemTTL = 10 * time.Minute
	}
	if idemMax <= 0 {
		idemMax = 50000
	}
	framesDir := filepath.Join(dataDir, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create frames dir: %w", err)
	}
	s := &FrameStore{
		root:          dataDir,
		framesDir:     framesDir,
		byIdempotency: map[string]*idemEntry{},
		idemOrder:     list.New(),
		idemTTL:       idemTTL,
		idemMax:       idemMax,
		metrics:       metrics,
	}
	s.updateMetrics()
	go s.cleanupLoop()
	return s, nil
}

func (s *FrameStore) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.cleanupExpired(time.Now().UTC())
	}
}

func (s *FrameStore) cleanupExpired(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, entry := range s.byIdempotency {
		if now.Sub(entry.seenAt) > s.idemTTL {
			s.removeIdemEntryLocked(k)
		}
	}
	s.updateMetricsLocked()
}

func (s *FrameStore) IsReady() bool {
	_, err := os.Stat(s.framesDir)
	return err == nil
}

func (s *FrameStore) SaveFrame(frameID, timestamp, idem, contentType string, payload []byte) (FrameMeta, error) {
	cleanID := sanitizeToken(frameID)
	if cleanID == "" {
		cleanID = fmt.Sprintf("frame-%d", time.Now().UnixNano())
	}
	if timestamp == "" {
		timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}

	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	if idem != "" {
		if existing, ok := s.byIdempotency[idem]; ok && now.Sub(existing.seenAt) <= s.idemTTL {
			meta := existing.frameMeta
			meta.Duplicate = true
			return meta, nil
		}
		if existing, ok := s.byIdempotency[idem]; ok && now.Sub(existing.seenAt) > s.idemTTL {
			s.removeIdemEntryLocked(idem)
		}
	}

	ext := extFromContentType(contentType)
	name := fmt.Sprintf("%s_%d%s", cleanID, time.Now().UnixNano(), ext)
	fullPath := filepath.Join(s.framesDir, name)
	if err := os.WriteFile(fullPath, payload, 0o644); err != nil {
		return FrameMeta{}, fmt.Errorf("write frame: %w", err)
	}
	sum := sha256.Sum256(payload)
	meta := FrameMeta{
		FrameID:        frameID,
		Timestamp:      timestamp,
		IdempotencyKey: idem,
		FileName:       name,
		Path:           fullPath,
		Size:           int64(len(payload)),
		ContentType:    contentType,
		SHA256:         hex.EncodeToString(sum[:]),
		ReceivedAt:     now,
	}
	s.last = meta
	s.count++
	if idem != "" {
		s.addIdemEntryLocked(idem, meta, now)
	}
	s.updateMetricsLocked()
	return meta, nil
}

func (s *FrameStore) addIdemEntryLocked(key string, meta FrameMeta, now time.Time) {
	elem := s.idemOrder.PushBack(key)
	s.byIdempotency[key] = &idemEntry{frameMeta: meta, seenAt: now, elem: elem}
	for len(s.byIdempotency) > s.idemMax {
		front := s.idemOrder.Front()
		if front == nil {
			break
		}
		oldKey, _ := front.Value.(string)
		s.removeIdemEntryLocked(oldKey)
		if s.metrics != nil {
			s.metrics.IdempotencyEvictionsTotal.Inc()
		}
	}
}

func (s *FrameStore) removeIdemEntryLocked(key string) {
	entry, ok := s.byIdempotency[key]
	if !ok {
		return
	}
	if entry.elem != nil {
		s.idemOrder.Remove(entry.elem)
	}
	delete(s.byIdempotency, key)
}

func (s *FrameStore) LastMeta() (FrameMeta, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.count
}

func (s *FrameStore) IdempotencySize() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byIdempotency)
}

func (s *FrameStore) RunCleanup(now time.Time) {
	s.cleanupExpired(now)
}

func (s *FrameStore) updateMetrics() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateMetricsLocked()
}

func (s *FrameStore) updateMetricsLocked() {
	if s.metrics != nil {
		s.metrics.IdempotencyEntries.Set(float64(len(s.byIdempotency)))
	}
}

func sanitizeToken(in string) string {
	trimmed := strings.TrimSpace(in)
	return safeToken.ReplaceAllString(trimmed, "_")
}

func extFromContentType(ct string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		return ".jpg"
	default:
		return ".bin"
	}
}

func ReadAllLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	lr := &io.LimitedReader{R: r, N: maxBytes + 1}
	b, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("payload too large")
	}
	return b, nil
}
