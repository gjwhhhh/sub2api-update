package codexeval

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Engine owns all mutable evaluation state. Credentials and account writes stay
// behind the callbacks supplied by the host; engines never share tasks or history.
type Engine struct {
	mu                          sync.Mutex
	sender                      Sender
	resolve                     Resolver
	active                      map[string]*run
	statePath                   string
	loaded                      bool
	stateLoadError              error
	storageError                string
	retryDelay                  time.Duration
	selectFingerprintChallenges func() []fingerprintChallenge
	fingerprintSlots            chan struct{}
	candyResults                map[string][]candyResult
	candyRunning                map[string]*candyProgress
	fingerprintResults          map[string][]fingerprintResult
	fingerprintRunning          map[string]*fingerprintProgress
	pelicanResults              map[string][]pelicanResult
	pelicanRunning              map[string]bool
	siwcResults                 map[string][]siwcResult
	siwcRunning                 map[string]bool
	inspection                  *inspector
	history                     HistoryStore
	workers                     sync.WaitGroup
	stopped                     bool
}

// Option configures an engine before any tasks or history are loaded.
type Option func(*Engine)

func WithHistoryStore(store HistoryStore) Option {
	return func(e *Engine) { e.history = store }
}

func New(dataDir string, send Sender, lookup Resolver, options ...Option) *Engine {
	e := &Engine{active: map[string]*run{}, retryDelay: 2 * time.Second,
		selectFingerprintChallenges: fingerprintChallenges, fingerprintSlots: make(chan struct{}, fingerprintGlobalConcurrency),
		candyResults: map[string][]candyResult{}, candyRunning: map[string]*candyProgress{},
		fingerprintResults: map[string][]fingerprintResult{}, fingerprintRunning: map[string]*fingerprintProgress{},
		pelicanResults: map[string][]pelicanResult{}, pelicanRunning: map[string]bool{},
		siwcResults: map[string][]siwcResult{}, siwcRunning: map[string]bool{}}
	for _, option := range options {
		option(e)
	}
	if dataDir != "" {
		e.initialize(dataDir, send, lookup)
	} else {
		e.sender, e.resolve = send, lookup
		if e.history != nil {
			e.loadState()
		}
	}
	return e
}

// Stop rejects new work, cancels manual and inspection tasks, and waits for their
// streams to close. It is idempotent; create a new engine to restart the module.
func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	e.stopped = true
	i := e.inspection
	if i != nil {
		i.cancel()
	}
	for _, r := range e.active {
		r.cancel()
	}
	e.mu.Unlock()
	done := make(chan struct{})
	go func() {
		if i != nil {
			<-i.done
		}
		e.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var ErrStopped = errors.New("evaluation module is stopped")
