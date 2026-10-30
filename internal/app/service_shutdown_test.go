package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitStoppedWaitsForRunnerToFinish(t *testing.T) {
	s := &Service{runnerDone: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if err := s.WaitStopped(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected wait while runner still alive, got %v", err)
	}
	close(s.runnerDone)
	if err := s.WaitStopped(context.Background()); err != nil {
		t.Fatalf("wait after runner stop: %v", err)
	}
}
