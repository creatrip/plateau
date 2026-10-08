// Package hostlock coordinates independent Plateau processes on one Mac.
package hostlock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

type Directory struct {
	Path    string
	Context context.Context
}

var resourceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

// Acquire never unlinks a lock file: replacing its inode would let a third
// process bypass an existing waiter. The OS releases flock after process death.
func (directory Directory) Acquire(resource string) (func(), error) {
	if directory.Path == "" || directory.Context == nil || !resourceName.MatchString(resource) {
		return nil, fmt.Errorf("invalid lock directory or resource %q", resource)
	}
	if err := directory.Context.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory.Path, 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(directory.Path, resource+".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s lock: %w", resource, err)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			var once sync.Once
			return func() {
				once.Do(func() {
					_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
					_ = file.Close()
				})
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire %s lock: %w", resource, err)
		}
		select {
		case <-directory.Context.Done():
			_ = file.Close()
			return nil, fmt.Errorf("wait for %s lock: %w", resource, directory.Context.Err())
		case <-ticker.C:
		}
	}
}
