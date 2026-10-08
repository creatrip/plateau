package process

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestRunnerBoundsHungCommandsAndInheritedOutputPipes(t *testing.T) {
	start := time.Now()
	runner := Runner{Context: context.Background(), Timeout: 50 * time.Millisecond}
	_, err := runner.Output("/bin/sh", []string{"-c", "sleep 30 & wait"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("subprocess output pipe outlived deadline")
	}
}

func TestRunnerPreservesExitCodeAndCancellation(t *testing.T) {
	runner := Runner{Context: context.Background()}
	err := runner.Run("/bin/sh", []string{"-c", "exit 17"}, nil, io.Discard, io.Discard)
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) || exit.ExitCode() != 17 {
		t.Fatalf("exit=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner.Context = ctx
	if _, err := runner.Output("/bin/sh", []string{"-c", "exit 0"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
