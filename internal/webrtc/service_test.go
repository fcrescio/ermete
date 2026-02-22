package webrtc

import "testing"

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
