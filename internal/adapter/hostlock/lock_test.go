package hostlock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This test body also serves as the subprocess entry point. Its messages are
// a pipe protocol, so parent tests never infer readiness from elapsed time.
func TestLockProcess(t *testing.T) {
	role := os.Getenv("PLATEAU_LOCK_TEST_ROLE")
	if role == "" {
		return
	}
	directory := os.Getenv("PLATEAU_LOCK_TEST_DIR")
	if role == "contender" {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		release, err := (Directory{Path: directory, Context: ctx}).Acquire("image")
		cancel()
		if release != nil {
			release()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("contending process acquired lock: %v", err)
		}
		fmt.Fprintln(os.Stdout, "blocked")
	} else if role != "holder" {
		t.Fatalf("unknown lock test role %q", role)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := (Directory{Path: directory, Context: ctx}).Acquire("image")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Fprintln(os.Stdout, "acquired")
	message, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil || message != "release\n" {
		t.Fatalf("release instruction = %q, error = %v", message, err)
	}
	release()
	fmt.Fprintln(os.Stdout, "released")
}

func startLockProcess(t *testing.T, directory, role string) (*exec.Cmd, io.WriteCloser, *bufio.Reader) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLockProcess$")
	child.Env = append(os.Environ(), "PLATEAU_LOCK_TEST_DIR="+directory, "PLATEAU_LOCK_TEST_ROLE="+role)
	child.Stderr = os.Stderr
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	return child, input, bufio.NewReader(output)
}

func expectLockMessage(t *testing.T, output *bufio.Reader, want string) {
	t.Helper()
	message, err := output.ReadString('\n')
	if err != nil || message != want+"\n" {
		t.Fatalf("child message = %q, error = %v; want %q", message, err, want)
	}
}

// The child really is another OS process: a race-detector-only test cannot
// prove that independent CLI commands exclude one another or survive a crash.
func TestLockAcrossProcessesAndCrash(t *testing.T) {
	directory := t.TempDir()
	child, _, output := startLockProcess(t, directory, "holder")
	expectLockMessage(t, output, "acquired")
	before, err := os.Stat(filepath.Join(directory, "image.lock"))
	if err != nil {
		t.Fatal(err)
	}

	blocked, cancelBlocked := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelBlocked()
	if release, err := (Directory{Path: directory, Context: blocked}).Acquire("image"); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("holder did not exclude another process: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locks := Directory{Path: directory, Context: ctx}
	other, err := locks.Acquire("container-a")
	if err != nil {
		t.Fatalf("unrelated resource was blocked: %v", err)
	}
	other()
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	var exitError *exec.ExitError
	if err := child.Wait(); !errors.As(err, &exitError) {
		t.Fatalf("killed holder error = %v, want process exit error", err)
	}

	again, err := locks.Acquire("image")
	if err != nil {
		t.Fatalf("dead holder retained lock: %v", err)
	}
	defer again()
	after, err := os.Stat(filepath.Join(directory, "image.lock"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock inode was replaced after holder death: %v", err)
	}
}

func TestLockHandsOffBetweenProcesses(t *testing.T) {
	directory := t.TempDir()
	holder, holderInput, holderOutput := startLockProcess(t, directory, "holder")
	expectLockMessage(t, holderOutput, "acquired")
	contender, contenderInput, contenderOutput := startLockProcess(t, directory, "contender")
	expectLockMessage(t, contenderOutput, "blocked")

	if _, err := io.WriteString(holderInput, "release\n"); err != nil {
		t.Fatal(err)
	}
	expectLockMessage(t, holderOutput, "released")
	expectLockMessage(t, contenderOutput, "acquired")
	if err := holder.Wait(); err != nil {
		t.Fatalf("holder: %v", err)
	}

	blocked, cancelBlocked := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelBlocked()
	if release, err := (Directory{Path: directory, Context: blocked}).Acquire("image"); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("contender did not own handed-off lock: %v", err)
	}
	if _, err := io.WriteString(contenderInput, "release\n"); err != nil {
		t.Fatal(err)
	}
	expectLockMessage(t, contenderOutput, "released")
	if err := contender.Wait(); err != nil {
		t.Fatalf("contender: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release, err := (Directory{Path: directory, Context: ctx}).Acquire("image")
	if err != nil {
		t.Fatalf("handed-off lock was not released: %v", err)
	}
	release()
}

func TestLockRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "image.lock")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if release, err := (Directory{Path: directory, Context: ctx}).Acquire("image"); err == nil {
		release()
		t.Fatal("accepted symlink as lock file")
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "untouched" {
		t.Fatalf("symlink target changed: content=%q, error=%v", content, err)
	}
}

func TestLockRejectsUnsafeNamesAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	locks := Directory{Path: t.TempDir(), Context: ctx}
	if _, err := locks.Acquire("../escape"); err == nil {
		t.Fatal("accepted path traversal")
	}
	if _, err := locks.Acquire("image"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
