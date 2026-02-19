package webrtc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

type Service struct {
	cfg      config.Config
	logger   *zap.Logger
	metrics  *observability.Metrics
	sessions *session.Manager
	store    *storage.FrameStore
	api      *pion.API
	upgrader websocket.Upgrader
	started  time.Time
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
		cfg:      cfg,
		logger:   logger,
		metrics:  metrics,
		sessions: sessions,
		store:    store,
		api:      api,
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		started:  time.Now().UTC(),
	}, nil
}

type PeerSession struct {
	id         string
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
	p.svc.sessions.Release(p.id)
}

func (s *Service) HandleWS(ctx context.Context, wsc *websocket.Conn) {
	s.metrics.WSConnectionsTotal.Inc()
	peer := &PeerSession{id: fmt.Sprintf("sess-%d", time.Now().UnixNano()), conn: wsc, logger: s.logger, svc: s}
	if err := s.sessions.Acquire(peer); err != nil {
		s.metrics.WSRejectTotal.Inc()
		_ = writeJSON(wsc, SignalMessage{Type: "error", Message: "session already active"})
		_ = wsc.Close()
		return
	}
	defer peer.Close("session_ended")

	if err := s.initPeer(peer); err != nil {
		peer.logger.Error("init peer failed", zap.Error(err))
		return
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
			if err := ps.outTrack.WriteRTP(pkt); err == nil {
				s.metrics.WebRTCPacketsOut.Inc()
			}
		}
	})
	pc.OnDataChannel(func(dc *pion.DataChannel) {
		if dc.Label() != "cmd" {
			return
		}
		ps.cmdChannel = dc
		dc.OnMessage(func(msg pion.DataChannelMessage) {
			s.handleCommand(ps, msg)
		})
	})
	_, err = pc.CreateDataChannel("cmd", nil)
	if err != nil {
		ps.logger.Warn("server cmd channel create failed", zap.Error(err))
	}
	return nil
}

func (s *Service) handleSignal(ps *PeerSession, msg SignalMessage) error {
	switch msg.Type {
	case "offer":
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

func (s *Service) handleCommand(ps *PeerSession, msg pion.DataChannelMessage) {
	if !msg.IsString {
		_ = ps.sendCmd(CommandEnvelope{Type: "pong", Bin: base64.StdEncoding.EncodeToString(msg.Data)})
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
		_ = ps.sendCmd(CommandEnvelope{Type: "say", Text: "audio loopback active"})
	default:
		_ = ps.sendCmd(CommandEnvelope{Type: "error", Text: "unknown command"})
	}
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
		return nil
	}
	b, _ := json.Marshal(msg)
	return p.cmdChannel.SendText(string(b))
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
