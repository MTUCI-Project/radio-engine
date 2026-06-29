package mp3stream

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPacerDoesNotAdvanceWhenInterrupted(t *testing.T) {
	p := newPacer(time.Now(), 0)
	commands := make(chan struct{})
	close(commands)

	err := p.wait(context.Background(), commands, time.Hour)
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("wait error = %v, want ErrSkipped", err)
	}
	if p.elapsed != 0 {
		t.Fatalf("elapsed = %s, want 0", p.elapsed)
	}
}

func TestPacerResetClearsElapsedTime(t *testing.T) {
	p := newPacer(time.Now().Add(-time.Second), 0)
	if err := p.wait(context.Background(), nil, time.Millisecond); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if p.elapsed == 0 {
		t.Fatal("elapsed was not advanced")
	}

	p.reset(time.Now(), 0)
	if p.elapsed != 0 {
		t.Fatalf("elapsed after reset = %s, want 0", p.elapsed)
	}
}
