package jev

import "sync"

// Breaker is the circuit breaker the client consults and updates. The
// persistent implementation lives in internal/breaker; MemoryBreaker is the
// default.
type Breaker interface {
	IsOpen() bool
	// Open force-opens the breaker (config errors: HTTP 401/422).
	Open(reason string)
	// RecordFailure counts a failure; two consecutive failures open it.
	RecordFailure(reason string)
	RecordSuccess()
}

// MemoryBreaker is an in-process Breaker.
type MemoryBreaker struct {
	mu          sync.Mutex
	open        bool
	consecutive int
	Reason      string
}

func (b *MemoryBreaker) IsOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}

func (b *MemoryBreaker) Open(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		b.open, b.Reason = true, reason
	}
}

func (b *MemoryBreaker) RecordFailure(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open {
		return
	}
	b.consecutive++
	if b.consecutive >= 2 {
		b.open, b.Reason = true, reason
	}
}

func (b *MemoryBreaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		b.consecutive = 0
	}
}
