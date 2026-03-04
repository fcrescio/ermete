package webrtc

import (
	"encoding/json"
	"testing"
)

func TestConnectionSnapshot(t *testing.T) {
	svc := &Service{consumers: map[string]*PeerSession{}}

	snap := svc.ConnectionSnapshot()
	if snap.ClientConnected {
		t.Fatalf("expected no client")
	}
	if snap.ConsumerCount != 0 {
		t.Fatalf("expected 0 consumers, got %d", snap.ConsumerCount)
	}

	svc.client = &PeerSession{id: "client-1"}
	svc.consumers["c1"] = &PeerSession{id: "c1"}
	svc.consumers["c2"] = &PeerSession{id: "c2"}

	snap = svc.ConnectionSnapshot()
	if !snap.ClientConnected {
		t.Fatalf("expected client connected")
	}
	if snap.ConsumerCount != 2 {
		t.Fatalf("expected 2 consumers, got %d", snap.ConsumerCount)
	}
}

func TestRelayDataMessageTypes(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
		want bool
	}{
		{name: "snapshot_description", body: map[string]any{"type": "snapshot_description", "producer": "teia"}, want: true},
		{name: "speaker_turn_completed", body: map[string]any{"type": "speaker_turn_completed", "producer": "ceo"}, want: true},
		{name: "Snapshot", body: map[string]any{"type": "Snapshot"}, want: true},
		{name: "PeriodicSnapshot", body: map[string]any{"type": "PeriodicSnapshot", "enable": true}, want: true},
		{name: "say_to_user", body: map[string]any{"type": "say_to_user", "text": "ciao"}, want: true},
		{name: "legacy_cmd", body: map[string]any{"type": "ping"}, want: false},
	}

	svc := &Service{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := svc.relayDataMessage(&PeerSession{id: "source"}, b)
			if got != tt.want {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestRelayDataMessageRejectsInvalidJSON(t *testing.T) {
	svc := &Service{}
	if svc.relayDataMessage(&PeerSession{id: "source"}, []byte("not-json")) {
		t.Fatalf("expected invalid json not to be treated as relayed data message")
	}
}
