package codexeval

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type failingSaveStore struct{ HistoryStore }

func (failingSaveStore) Save([]byte) error { return errors.New("storage unavailable") }

func TestEngineIsolationAndHistoryReload(t *testing.T) {
	resolve := func(context.Context, int64) (string, error) { return "fixture", nil }
	send := func(context.Context, int64, []byte, http.Header) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"21\"}]}]}}\n\n"))}, nil
	}
	dir := t.TempDir()
	a, b := New(dir, send, resolve), New(t.TempDir(), send, resolve)
	t.Cleanup(func() { _ = a.Stop(context.Background()); _ = b.Stop(context.Background()) })
	req := StartRequest{Type: "candy", Model: "fixture", Runs: 1, Targets: []Target{{GroupID: 1, AccountID: 7}}}
	for _, e := range []*Engine{a, b} {
		s, err := e.Start(context.Background(), req)
		if err != nil || len(s.Started) != 1 {
			t.Fatalf("independent admission: %+v, %v", s, err)
		}
		e.waitIdle(t)
	}
	if len(a.candyResults["7"]) != 1 || len(b.candyResults["7"]) != 1 {
		t.Fatal("instances shared results")
	}
	if _, status := a.Clear("all"); status != 200 || len(b.candyResults["7"]) != 1 {
		t.Fatal("clearing an instance affected another")
	}
	reloaded := New("", nil, nil, WithHistoryStore(JSONHistoryStore{Path: b.statePath}))
	if len(reloaded.candyResults["7"]) != 1 || reloaded.candyResults["7"][0].RunID != b.candyResults["7"][0].RunID {
		t.Fatal("history store did not restore the same record")
	}
	info, err := os.Stat(b.statePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("history permissions: %v, %v", info, err)
	}
}

func TestEngineStopCancelsTasksAndRejectsRestart(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	e := New(t.TempDir(), func(ctx context.Context, _ int64, _ []byte, _ http.Header) (*http.Response, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}, func(context.Context, int64) (string, error) { return "fixture", nil })
	req := StartRequest{Type: "candy", Model: "fixture", Runs: 1, Targets: []Target{{1, 7}}}
	if _, err := e.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("sender not started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.Stop(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	select {
	case <-cancelled:
	default:
		t.Fatal("Stop returned before sender cancellation")
	}
	if len(e.active) != 0 {
		t.Fatal("Stop left active tasks")
	}
	if _, err := e.Start(ctx, req); !errors.Is(err, ErrStopped) {
		t.Fatalf("restart accepted: %v", err)
	}
}

func TestEngineConcurrentInspectionLifecycle(t *testing.T) {
	e := New("", nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.InitializeInspection(InspectionHooks{})
			e.UpdateInspectionSettings(DefaultInspectionSettings())
			_ = e.CurrentInspectionState()
			e.CancelInspection()
			if err := e.Stop(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	e.InitializeInspection(InspectionHooks{})
	if err := e.StartInspection(); err == nil {
		t.Fatal("stopped inspection restarted")
	}
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e := New("", nil, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return e
}
