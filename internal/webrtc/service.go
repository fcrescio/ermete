package webrtc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"ermete/internal/config"
	"ermete/internal/observability"
	"ermete/internal/session"
	"ermete/internal/storage"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
	"go.uber.org/zap"
)

type SignalMessage struct {
	Type      string                 `json:"type"`
	SDP       string                 `json:"sdp,omitempty"`
	Candidate *pion.ICECandidateInit `json:"candidate,omitempty"`
	Message   string                 `json:"message,omitempty"`
}

type CommandEnvelope struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Bin  string `json:"bin,omitempty"`
}

var relayedDataMessageTypes = map[string]struct{}{
	"snapshot_description":   {},
	"speaker_turn_completed": {},
	"Snapshot":               {},
	"PeriodicSnapshot":       {},
	"say_to_user":            {},
}

type Service struct {
	cfg       config.Config
	logger    *zap.Logger
	metrics   *observability.Metrics
	sessions  *session.Manager
	store     *storage.FrameStore
	api       *pion.API
	upgrader  websocket.Upgrader
	started   time.Time
	mu        sync.RWMutex
	client    *PeerSession
	consumers map[string]*PeerSession
}

type ConnectionSnapshot struct {
	ClientConnected bool
	ConsumerCount   int
}

func NewService(cfg config.Config, logger *zap.Logger, metrics *observability.Metrics, sessions *session.Manager, store *storage.FrameStore) (*Service, error) {
	m := &pion.MediaEngine{}
	if err := m.RegisterCodec(pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeOpus, ClockRate: 48000, Channels: 2}, PayloadType: 111}, pion.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	se := pion.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	api := pion.NewAPI(pion.WithMediaEngine(m), pion.WithSettingEngine(se))
	return &Service{
		cfg:       cfg,
		logger:    logger,
		metrics:   metrics,
		sessions:  sessions,
		store:     store,
		api:       api,
		upgrader:  websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		started:   time.Now().UTC(),
		consumers: map[string]*PeerSession{},
	}, nil
}

type PeerSession struct {
	id         string
	role       string
	conn       *websocket.Conn
	pc         *pion.PeerConnection
	outTrack   *pion.TrackLocalStaticRTP
	cmdChannel *pion.DataChannel
	logger     *zap.Logger
	svc        *Service
	mu         sync.Mutex
	closed     bool
}

func (p *PeerSession) ID() string { return p.id }

func (p *PeerSession) Close(reason string) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	_ = p.sendSignal(SignalMessage{Type: "error", Message: reason})
	_ = p.sendSignal(SignalMessage{Type: "bye"})
	if p.pc != nil {
		_ = p.pc.Close()
	}
	_ = p.conn.Close()
	p.svc.onPeerClosed(p)
}

func (s *Service) HandleWS(ctx context.Context, wsc *websocket.Conn, role string) {
	s.metrics.WSConnectionsTotal.Inc()
	peer := &PeerSession{id: fmt.Sprintf("sess-%d", time.Now().UnixNano()), role: role, conn: wsc, logger: s.logger, svc: s}
	if err := s.registerPeer(peer); err != nil {
		s.metrics.WSRejectTotal.Inc()
		_ = writeJSON(wsc, SignalMessage{Type: "error", Message: err.Error()})
		_ = wsc.Close()
		return
	}
	defer peer.Close("session_ended")

	if err := s.initPeer(peer); err != nil {
		peer.logger.Error("init peer failed", zap.Error(err))
		return
	}
	if role == "consumer" {
		if err := s.sendOffer(peer); err != nil {
			peer.logger.Error("send offer to consumer failed", zap.Error(err))
			return
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, b, err := wsc.ReadMessage()
		if err != nil {
			return
		}
		s.sessions.Touch()
		var msg SignalMessage
		if err := json.Unmarshal(b, &msg); err != nil {
			_ = peer.sendSignal(SignalMessage{Type: "error", Message: "invalid json"})
			continue
		}
		if err := s.handleSignal(peer, msg); err != nil {
			peer.logger.Warn("signal error", zap.Error(err))
			_ = peer.sendSignal(SignalMessage{Type: "error", Message: err.Error()})
		}
	}
}

func (s *Service) initPeer(ps *PeerSession) error {
	cfg := pion.Configuration{ICEServers: s.iceServers()}
	pc, err := s.api.NewPeerConnection(cfg)
	if err != nil {
		return err
	}
	ps.pc = pc
	track, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "ermete")
	if err != nil {
		return err
	}
	ps.outTrack = track
	if _, err := pc.AddTrack(track); err != nil {
		return err
	}
	pc.OnICECandidate(func(c *pion.ICECandidate) {
		if c == nil {
			return
		}
		cand := c.ToJSON()
		_ = ps.sendSignal(SignalMessage{Type: "candidate", Candidate: &cand})
	})
	pc.OnICEConnectionStateChange(func(st pion.ICEConnectionState) {
		ps.logger.Info("ice state", zap.String("state", st.String()))
		if st == pion.ICEConnectionStateConnected || st == pion.ICEConnectionStateCompleted {
			s.sessions.SetState(session.StateConnected)
		}
		if st == pion.ICEConnectionStateDisconnected || st == pion.ICEConnectionStateFailed {
			s.sessions.SetState(session.StateConnecting)
		}
	})
	pc.OnConnectionStateChange(func(st pion.PeerConnectionState) {
		if st == pion.PeerConnectionStateFailed || st == pion.PeerConnectionStateClosed {
			ps.Close("peer_connection_closed")
		}
	})
	pc.OnTrack(func(remote *pion.TrackRemote, _ *pion.RTPReceiver) {
		if remote.Kind() != pion.RTPCodecTypeAudio {
			return
		}
		for {
			pkt, _, err := remote.ReadRTP()
			if err != nil {
				return
			}
			s.metrics.WebRTCPacketsIn.Inc()
			s.sessions.Touch()
			s.forwardAudio(ps, pkt)
		}
	})
	pc.OnDataChannel(func(dc *pion.DataChannel) {
		if dc.Label() != "cmd" {
			ps.logger.Debug("ignoring datachannel with unexpected label", zap.String("label", dc.Label()))
			return
		}
		s.bindCommandChannel(ps, dc, "remote")
	})
	cmdDC, err := pc.CreateDataChannel("cmd", nil)
	if err != nil {
		ps.logger.Warn("server cmd channel create failed", zap.Error(err))
	} else {
		s.bindCommandChannel(ps, cmdDC, "local")
	}
	return nil
}

func (s *Service) bindCommandChannel(ps *PeerSession, dc *pion.DataChannel, origin string) {
	ps.cmdChannel = dc
	ps.logger.Info("cmd datachannel bound",
		zap.String("peer_id", ps.id),
		zap.String("role", ps.role),
		zap.String("origin", origin),
		zap.String("label", dc.Label()),
	)
	dc.OnOpen(func() {
		ps.logger.Info("cmd datachannel open",
			zap.String("peer_id", ps.id),
			zap.String("role", ps.role),
			zap.String("origin", origin),
			zap.String("label", dc.Label()),
			zap.String("state", dc.ReadyState().String()),
		)
		if err := ps.sendCmd(CommandEnvelope{Type: "server_status", Text: "cmd_channel_open"}); err != nil {
			ps.logger.Warn("failed to send cmd channel open probe", zap.Error(err))
		}
	})
	dc.OnClose(func() {
		ps.logger.Warn("cmd datachannel closed",
			zap.String("peer_id", ps.id),
			zap.String("role", ps.role),
			zap.String("origin", origin),
			zap.String("label", dc.Label()),
			zap.String("state", dc.ReadyState().String()),
		)
	})
	dc.OnError(func(err error) {
		ps.logger.Error("cmd datachannel error",
			zap.String("peer_id", ps.id),
			zap.String("role", ps.role),
			zap.String("origin", origin),
			zap.String("label", dc.Label()),
			zap.Error(err),
		)
	})
	dc.OnMessage(func(msg pion.DataChannelMessage) {
		s.handleCommand(ps, msg)
	})
}

func (s *Service) handleSignal(ps *PeerSession, msg SignalMessage) error {
	switch msg.Type {
	case "offer":
		if ps.role == "consumer" {
			return errors.New("unexpected offer for consumer")
		}
		if msg.SDP == "" {
			return errors.New("missing offer sdp")
		}
		offer := pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: msg.SDP}
		if err := ps.pc.SetRemoteDescription(offer); err != nil {
			return err
		}
		answer, err := ps.pc.CreateAnswer(nil)
		if err != nil {
			return err
		}
		if err := ps.pc.SetLocalDescription(answer); err != nil {
			return err
		}
		return ps.sendSignal(SignalMessage{Type: "answer", SDP: answer.SDP})
	case "answer":
		if ps.role != "consumer" {
			return errors.New("unexpected answer")
		}
		if msg.SDP == "" {
			return errors.New("missing answer sdp")
		}
		answer := pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: msg.SDP}
		return ps.pc.SetRemoteDescription(answer)
	case "candidate":
		if msg.Candidate == nil {
			return errors.New("missing candidate")
		}
		return ps.pc.AddICECandidate(*msg.Candidate)
	case "bye":
		ps.Close("remote_bye")
		return nil
	default:
		return fmt.Errorf("unknown signal type: %s", msg.Type)
	}
}

func (s *Service) registerPeer(ps *PeerSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ps.role == "consumer" {
		s.consumers[ps.id] = ps
		return nil
	}
	if err := s.sessions.Acquire(ps); err != nil {
		return errors.New("session already active")
	}
	s.client = ps
	return nil
}

func (s *Service) onPeerClosed(ps *PeerSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ps.role == "consumer" {
		delete(s.consumers, ps.id)
		return
	}
	if s.client != nil && s.client.id == ps.id {
		s.client = nil
	}
	s.sessions.Release(ps.id)
}

func (s *Service) ConnectionSnapshot() ConnectionSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ConnectionSnapshot{
		ClientConnected: s.client != nil,
		ConsumerCount:   len(s.consumers),
	}
}

func (s *Service) StartConnectionLogging(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snap := s.ConnectionSnapshot()
				s.logger.Info("webrtc connection status",
					zap.Bool("client_connected", snap.ClientConnected),
					zap.Int("consumer_count", snap.ConsumerCount),
				)
			}
		}
	}()
}

func (s *Service) sendOffer(ps *PeerSession) error {
	offer, err := ps.pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := ps.pc.SetLocalDescription(offer); err != nil {
		return err
	}
	return ps.sendSignal(SignalMessage{Type: "offer", SDP: offer.SDP})
}

func (s *Service) forwardAudio(src *PeerSession, pkt *rtp.Packet) {
	cp := CloneRTP(pkt)
	s.mu.RLock()
	client := s.client
	consumers := make([]*PeerSession, 0, len(s.consumers))
	for _, c := range s.consumers {
		consumers = append(consumers, c)
	}
	s.mu.RUnlock()

	if src.role == "consumer" {
		if client != nil && client.outTrack != nil {
			if err := client.outTrack.WriteRTP(CloneRTP(cp)); err == nil {
				s.metrics.WebRTCPacketsOut.Inc()
			}
		}
		return
	}
	for _, c := range consumers {
		if c != nil && c.outTrack != nil {
			if err := c.outTrack.WriteRTP(CloneRTP(cp)); err == nil {
				s.metrics.WebRTCPacketsOut.Inc()
			}
		}
	}
}

func (s *Service) NotifyFrameAvailable(meta storage.FrameMeta, publicBaseURL string) {
	payload := map[string]any{
		"type":         "frame_available",
		"frame_id":     meta.FrameID,
		"file_name":    meta.FileName,
		"download_url": strings.TrimRight(publicBaseURL, "/") + "/v1/frames/file/" + meta.FileName,
	}
	s.mu.RLock()
	consumers := make([]*PeerSession, 0, len(s.consumers))
	for _, c := range s.consumers {
		consumers = append(consumers, c)
	}
	s.mu.RUnlock()
	for _, c := range consumers {
		if c != nil {
			_ = writeJSON(c.conn, payload)
		}
	}
}

func (s *Service) handleCommand(ps *PeerSession, msg pion.DataChannelMessage) {
	if !msg.IsString {
		_ = ps.sendCmd(CommandEnvelope{Type: "pong", Bin: base64.StdEncoding.EncodeToString(msg.Data)})
		return
	}
	if s.relayDataMessage(ps, msg.Data) {
		return
	}
	var env CommandEnvelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		_ = ps.sendCmd(CommandEnvelope{Type: "error", Text: "invalid command envelope"})
		return
	}
	switch env.Type {
	case "ping":
		_ = ps.sendCmd(CommandEnvelope{Type: "pong", Text: "ok"})
	case "server_status":
		last, count := s.store.LastMeta()
		snap := s.sessions.Snapshot()
		payload := map[string]any{"session": snap, "last_frame": last, "frames_count": count, "uptime_seconds": int(time.Since(s.started).Seconds())}
		b, _ := json.Marshal(payload)
		_ = ps.sendCmd(CommandEnvelope{Type: "server_status", Text: string(b)})
	case "say":
		_ = ps.sendCmd(CommandEnvelope{Type: "say", Text: "audio forwarding active"})
	default:
		_ = ps.sendCmd(CommandEnvelope{Type: "error", Text: "unknown command"})
	}
}

func (s *Service) relayDataMessage(src *PeerSession, data []byte) bool {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	typ, _ := raw["type"].(string)
	if _, ok := relayedDataMessageTypes[typ]; !ok {
		return false
	}

	s.mu.RLock()
	client := s.client
	consumers := make([]*PeerSession, 0, len(s.consumers))
	for _, c := range s.consumers {
		consumers = append(consumers, c)
	}
	s.mu.RUnlock()

	if client != nil && client.id != src.id {
		_ = client.sendRawDataText(string(data))
	}
	for _, c := range consumers {
		if c == nil || c.id == src.id {
			continue
		}
		_ = c.sendRawDataText(string(data))
	}
	return true
}

func (s *Service) iceServers() []pion.ICEServer {
	out := make([]pion.ICEServer, 0, 2)
	if len(s.cfg.WebRTCStunURLs) > 0 {
		out = append(out, pion.ICEServer{URLs: s.cfg.WebRTCStunURLs})
	}
	if len(s.cfg.WebRTCTurnURLs) > 0 {
		out = append(out, pion.ICEServer{URLs: s.cfg.WebRTCTurnURLs, Username: s.cfg.WebRTCTurnUser, Credential: s.cfg.WebRTCTurnPass})
	}
	return out
}

func (p *PeerSession) sendSignal(msg SignalMessage) error { return writeJSON(p.conn, msg) }

func (p *PeerSession) sendCmd(msg CommandEnvelope) error {
	if p.cmdChannel == nil {
		p.logger.Debug("cmd channel unavailable: message dropped", zap.String("type", msg.Type))
		return nil
	}
	if p.cmdChannel.ReadyState() != pion.DataChannelStateOpen {
		p.logger.Debug("cmd channel not open yet: message dropped",
			zap.String("type", msg.Type),
			zap.String("state", p.cmdChannel.ReadyState().String()),
		)
		return nil
	}
	b, _ := json.Marshal(msg)
	return p.cmdChannel.SendText(string(b))
}

func (p *PeerSession) sendRawDataText(payload string) error {
	if p.cmdChannel == nil {
		return nil
	}
	return p.cmdChannel.SendText(payload)
}

func writeJSON(conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, b)
}

func CloneRTP(pkt *rtp.Packet) *rtp.Packet {
	cp := *pkt
	cp.Payload = append([]byte(nil), pkt.Payload...)
	return &cp
}
