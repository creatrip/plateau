// Package process bounds external commands and preserves their exit status.
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type Runner struct {
	Context     context.Context
	Timeout     time.Duration
	Interactive bool
}

func (runner Runner) Run(command string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	ctx := runner.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if !runner.Interactive {
		timeout := runner.Timeout
		if timeout == 0 {
			timeout = 30 * time.Minute
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	process := exec.CommandContext(ctx, command, args...)
	process.Stdin, process.Stdout, process.Stderr = stdin, stdout, stderr
	process.WaitDelay = 2 * time.Second
	if !runner.Interactive {
		process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// Shell grandchildren may keep output pipes open after their parent
		// dies. Cancel the command group, never an unrelated managed service.
		process.Cancel = func() error { return syscall.Kill(-process.Process.Pid, syscall.SIGKILL) }
	}
	if err := process.Run(); err != nil {
		return fmt.Errorf("run %s: %w", command, errors.Join(err, ctx.Err()))
	}
	return nil
}

func (runner Runner) Output(command string, args []string) ([]byte, error) {
	if runner.Timeout == 0 {
		runner.Timeout = 30 * time.Second
	}
	var output, stderr bytes.Buffer
	if err := runner.Run(command, args, nil, &output, &stderr); err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("%w: %s", err, message)
		}
		return nil, err
	}
	return output.Bytes(), nil
}
