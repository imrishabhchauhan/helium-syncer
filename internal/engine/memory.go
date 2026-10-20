package engine

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
)

// Memory is an in-process Transport for tests: one shared op log.
type Memory struct {
	mu       sync.Mutex
	log      []crdt.Op
	versions map[string]string // device id -> Helium version
	names    map[string]string
	// FailPush makes the next Push fail (to test the outbox).
	FailPush bool
}

// NewMemory returns an empty in-memory server.
func NewMemory() *Memory {
	return &Memory{versions: map[string]string{}, names: map[string]string{}}
}

func (m *Memory) Push(_ context.Context, ops []crdt.Op) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailPush {
		m.FailPush = false
		return errOffline
	}
	for _, op := range ops {
		// Copy, as a real server would serialise.
		b, _ := json.Marshal(op)
		var c crdt.Op
		json.Unmarshal(b, &c)
		m.log = append(m.log, c)
	}
	return nil
}

func (m *Memory) Pull(_ context.Context, cursor string) ([]crdt.Op, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	from, _ := strconv.Atoi(cursor)
	if from > len(m.log) {
		from = len(m.log)
	}
	out := append([]crdt.Op(nil), m.log[from:]...)
	return out, strconv.Itoa(len(m.log)), nil
}

func (m *Memory) Versions(_ context.Context, except string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for id, v := range m.versions {
		if id != except {
			out[m.names[id]] = v
		}
	}
	return out, nil
}

func (m *Memory) Announce(_ context.Context, device, name, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.versions[device], m.names[device] = version, name
	return nil
}

// Ops returns how many ops the server holds.
func (m *Memory) Ops() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.log)
}

type offlineError struct{}

func (offlineError) Error() string { return "server unreachable" }

var errOffline = offlineError{}
