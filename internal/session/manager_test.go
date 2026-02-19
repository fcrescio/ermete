package session

import (
	"testing"

	"ermete/internal/config"
)

type fakeSession struct {
	id     string
	closed bool
}

func (f *fakeSession) ID() string     { return f.id }
func (f *fakeSession) Close(_ string) { f.closed = true }

func TestRejectPolicy(t *testing.T) {
	m := NewManager(config.SessionPolicyRejectSecond)
	a := &fakeSession{id: "a"}
	b := &fakeSession{id: "b"}
	if err := m.Acquire(a); err != nil {
		t.Fatal(err)
	}
	if err := m.Acquire(b); err == nil {
		t.Fatal("expected reject error")
	}
}

func TestKickPolicy(t *testing.T) {
	m := NewManager(config.SessionPolicyKickPrevious)
	a := &fakeSession{id: "a"}
	b := &fakeSession{id: "b"}
	if err := m.Acquire(a); err != nil {
		t.Fatal(err)
	}
	if err := m.Acquire(b); err != nil {
		t.Fatal(err)
	}
	if !a.closed {
		t.Fatal("expected previous session to close")
	}
}
