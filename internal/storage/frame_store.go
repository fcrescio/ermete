package storage

import (
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

type FrameStore struct {
	root          string
	framesDir     string
	mu            sync.Mutex
	byIdempotency map[string]FrameMeta
	last          FrameMeta
	count         uint64
}

func NewFrameStore(dataDir string) (*FrameStore, error) {
	framesDir := filepath.Join(dataDir, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create frames dir: %w", err)
	}
	return &FrameStore{root: dataDir, framesDir: framesDir, byIdempotency: map[string]FrameMeta{}}, nil
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

	s.mu.Lock()
	defer s.mu.Unlock()

	if idem != "" {
		if existing, ok := s.byIdempotency[idem]; ok {
			existing.Duplicate = true
			return existing, nil
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
		ReceivedAt:     time.Now().UTC(),
	}
	s.last = meta
	s.count++
	if idem != "" {
		s.byIdempotency[idem] = meta
	}
	return meta, nil
}

func (s *FrameStore) LastMeta() (FrameMeta, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.count
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
