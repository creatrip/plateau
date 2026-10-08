package incus

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/creatrip/plateau/internal/application"
	"github.com/creatrip/plateau/internal/domain"
)

// 호출자는 변경 전후 이름의 컨테이너 잠금을 모두 보유해야 합니다.
func (manager Manager) Rename(oldName, newName domain.Name) error {
	if _, err := domain.ParseName(oldName.String()); err != nil {
		return err
	}
	if _, err := domain.ParseName(newName.String()); err != nil {
		return err
	}
	if oldName == newName {
		return fmt.Errorf("new name must differ from the current name")
	}
	source, sourceErr := manager.Inspect(oldName)
	if sourceErr != nil && !errors.Is(sourceErr, domain.ErrNotFound) {
		return sourceErr
	}
	target, targetErr := manager.Inspect(newName)
	if targetErr != nil && !errors.Is(targetErr, domain.ErrNotFound) {
		return targetErr
	}
	hostsContent := source.RenameHosts
	if sourceErr == nil {
		if targetErr == nil {
			return fmt.Errorf("%w: %s", application.ErrAlreadyExists, newName)
		}
		if source.RuntimeStatus != domain.StatusStopped || source.DesiredState != domain.DesiredStopped {
			return fmt.Errorf("stop %s before renaming it", oldName)
		}
		if source.PendingOperation != "" && (source.PendingOperation != "rename" || source.RenameFrom != oldName || source.RenameTo != newName) {
			return fmt.Errorf("%w: container %q has another pending operation", domain.ErrInvalidState, oldName)
		}
		if source.PendingOperation == "" {
			hosts, err := manager.transport.Output([]string{"sudo", "incus", "file", "pull", oldName.String() + "/etc/hosts", "-"})
			if err != nil {
				return fmt.Errorf("read guest hosts before rename: %w", err)
			}
			if len(hosts) > 1<<20 || !utf8.Valid(hosts) {
				return fmt.Errorf("guest hosts file is not valid UTF-8 text within 1 MiB")
			}
			lines := strings.Split(string(hosts), "\n")
			for index, line := range lines {
				entry, comment, hasComment := strings.Cut(line, "#")
				fields := strings.Fields(entry)
				changed := false
				for i := 1; i < len(fields); i++ {
					if fields[i] == oldName.String() {
						fields[i] = newName.String()
						changed = true
					}
				}
				if changed {
					lines[index] = strings.Join(fields, "\t")
					if hasComment {
						lines[index] += " #" + comment
					}
				}
			}
			hostsContent = strings.Join(lines, "\n")
			// 파일 쓰기가 중단돼도 원본에서 만든 내용을 복구할 수 있게 먼저 저장합니다.
			encodedHosts, err := json.Marshal(hostsContent)
			if err != nil {
				return fmt.Errorf("encode hostname checkpoint: %w", err)
			}
			if err := manager.runCommand([]string{"sudo", "incus", "config", "set", oldName.String(), "user.plateau.pending=rename", "user.plateau.rename-from=" + oldName.String(), "user.plateau.rename-to=" + newName.String(), "user.plateau.rename-hosts=" + string(encodedHosts)}, nil, io.Discard, "", ""); err != nil {
				return fmt.Errorf("record rename checkpoint: %w", err)
			}
		}
		if err := manager.runCommand([]string{"sudo", "incus", "move", oldName.String(), newName.String()}, nil, io.Discard, "", ""); err != nil {
			return fmt.Errorf("rename container: %w", err)
		}
	} else {
		if targetErr != nil {
			return sourceErr
		}
		// A completed request is idempotent only when the destination retains proof
		// of this source name. An unrelated namesake must never count as success.
		if target.PendingOperation == "" && target.RenamedFrom == oldName {
			return nil
		}
		if target.PendingOperation != "rename" || target.RenameFrom != oldName || target.RenameTo != newName {
			return fmt.Errorf("%w: destination is not this pending rename", application.ErrAlreadyExists)
		}
		hostsContent = target.RenameHosts
	}
	for _, file := range []struct{ path, content string }{
		{"etc/hostname", newName.String() + "\n"}, {"etc/hosts", hostsContent},
	} {
		if err := manager.runCommand([]string{"sudo", "incus", "file", "push", "-", newName.String() + "/" + file.path, "--uid", "0", "--gid", "0", "--mode", "0644"}, strings.NewReader(file.content), io.Discard, "", ""); err != nil {
			return fmt.Errorf("update guest %s (retry rename): %w", file.path, err)
		}
	}
	// Keep the checkpoint until local cleanup succeeds. Names are validated and
	// both are locked, so only these two per-container trust records are removed.
	if manager.sshDirectory == "" {
		return fmt.Errorf("locate Plateau SSH configuration directory")
	}
	for _, name := range []domain.Name{oldName, newName} {
		if err := os.Remove(filepath.Join(manager.sshDirectory, "known_hosts-"+name.String())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale SSH host key for %q: %w", name, err)
		}
	}
	if err := manager.runCommand([]string{"sudo", "incus", "config", "set", newName.String(), "user.plateau.name=" + newName.String(), "user.plateau.pending=", "user.plateau.rename-from=", "user.plateau.rename-to=", "user.plateau.rename-hosts=", "user.plateau.renamed-from=" + oldName.String()}, nil, io.Discard, "", ""); err != nil {
		return fmt.Errorf("complete rename metadata: %w", err)
	}
	return nil
}
