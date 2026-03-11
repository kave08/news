package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunServicesCancelsSiblingOnError(t *testing.T) {
	t.Parallel()

	blocking := &stubRunner{run: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	failing := &stubRunner{run: func(context.Context) error {
		return errors.New("boom")
	}}

	err := runServices(context.Background(), map[string]serviceRunner{
		"relay": failing,
		"news":  blocking,
	})
	if err == nil || err.Error() != "relay service: boom" {
		t.Fatalf("unexpected error: %v", err)
	}
	if !blocking.canceled {
		t.Fatal("expected sibling runner to observe cancellation")
	}
}

func TestRunServicesPropagatesContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runServices(ctx, map[string]serviceRunner{
		"relay": &stubRunner{run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestNewBaleHTTPClientAppliesMinimumTimeout(t *testing.T) {
	t.Parallel()

	client := newBaleHTTPClient(10 * time.Second)
	if client.Timeout != time.Minute {
		t.Fatalf("unexpected timeout: %s", client.Timeout)
	}
}

func TestNewBaleHTTPClientExtendsLongPollTimeout(t *testing.T) {
	t.Parallel()

	client := newBaleHTTPClient(90 * time.Second)
	if client.Timeout != 135*time.Second {
		t.Fatalf("unexpected timeout: %s", client.Timeout)
	}
}

type stubRunner struct {
	canceled bool
	run      func(context.Context) error
}

func (s *stubRunner) Run(ctx context.Context) error {
	done := make(chan struct{})
	var err error
	go func() {
		err = s.run(ctx)
		if errors.Is(err, context.Canceled) {
			s.canceled = true
		}
		close(done)
	}()

	select {
	case <-done:
		return err
	case <-time.After(500 * time.Millisecond):
		return errors.New("runner timeout")
	}
}
