package session

import (
	"errors"
	"sync"
	"time"

	"ermete/internal/config"
)

var ErrSessionAlreadyActive = errors.New("session already active")

type State string

const (
	StateDisconnected State = "disconnected"
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
)

type SessionRef interface {
	ID() string
	Close(reason string)
}

type Snapshot struct {
	State      State     `json:"state"`
	SessionID  string    `json:"session_id,omitempty"`
	LastActive time.Time `json:"last_active"`
}

type Manager struct {
	policy     config.SessionPolicy
	mu         sync.Mutex
	state      State
	active     SessionRef
	lastActive time.Time
}

func NewManager(policy config.SessionPolicy) *Manager {
	return &Manager{policy: policy, state: StateDisconnected}
}

func (m *Manager) Acquire(s SessionRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.active != nil {
		if m.policy == config.SessionPolicyRejectSecond {
			return ErrSessionAlreadyActive
		}
		m.active.Close("replaced_by_new_session")
	}
	m.active = s
	m.state = StateConnecting
	m.lastActive = time.Now().UTC()
	return nil
}

func (m *Manager) SetState(state State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = state
	m.lastActive = time.Now().UTC()
}

func (m *Manager) Touch() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastActive = time.Now().UTC()
}

func (m *Manager) Release(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil && m.active.ID() == sessionID {
		m.active = nil
		m.state = StateDisconnected
		m.lastActive = time.Now().UTC()
	}
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Snapshot{State: m.state, LastActive: m.lastActive}
	if m.active != nil {
		s.SessionID = m.active.ID()
	}
	return s
}
