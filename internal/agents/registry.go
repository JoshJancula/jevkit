package agents

import "sync"

var (
	regMu sync.RWMutex
	reg   = map[string]Agent{}
)

// Register adds an adapter under its Name(). Later registrations replace
// earlier ones for the same name.
func Register(a Agent) {
	if a == nil {
		return
	}
	regMu.Lock()
	defer regMu.Unlock()
	reg[a.Name()] = a
}

// Lookup returns the adapter registered as name, or nil.
func Lookup(name string) Agent {
	regMu.RLock()
	defer regMu.RUnlock()
	return reg[name]
}

// Names returns registered adapter names in unspecified order.
func Names() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(reg))
	for n := range reg {
		out = append(out, n)
	}
	return out
}
