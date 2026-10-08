package application

import (
	"errors"
	"fmt"
	"sort"

	"github.com/creatrip/plateau/internal/domain"
)

var (
	ErrAlreadyExists   = errors.New("instance already exists")
	ErrInstanceRunning = errors.New("instance is running")
)

type shellExitError struct{ code int }

func (exit shellExitError) Error() string {
	return fmt.Sprintf("remote shell exited with status %d", exit.code)
}
func (exit shellExitError) ShellExitCode() int { return exit.code }

// Locker excludes other CLI processes, not just goroutines. Session lifetimes
// are deliberately outside the lock; only preparation and mutations hold it.
type Locker interface {
	Acquire(resource string) (release func(), err error)
}

// InstanceRuntime owns platform mechanisms. The application owns operation
// ordering, restart intent and retry contracts; it has no second state store.
type InstanceRuntime interface {
	Inspect(name domain.Name) (domain.InstanceStatus, error)
	List() ([]domain.InstanceStatus, error)
	AllocateVNCPort() (uint16, error)
	Create(name domain.Name, vncPort uint16, group string) error
	SetGroup(name domain.Name, group string) error
	Rename(oldName, newName domain.Name) error
	DiskUsage(names []domain.Name) (map[domain.Name]uint64, error)
	Start(name domain.Name) error
	Stop(name domain.Name) error
	PrepareSSH(name domain.Name, vncPort uint16) error
	Complete(name domain.Name) error
	SSH(name domain.Name, vncPort uint16) error
	OpenVNC(port uint16) error
	Delete(name domain.Name) error
	EnableAutostart(name domain.Name) error
	DisableAutostart(name domain.Name) error
	Backup(instance domain.Instance, destination string) error
	ReadBackupManifest(path string) (domain.Instance, error)
	RestoreBackup(path string, instance domain.Instance) error
	UpdateImage() error
	UpdateGuest(name domain.Name) error
}

type Instances struct {
	runtime InstanceRuntime
	locks   Locker
}

func NewInstances(runtime InstanceRuntime, locks Locker) Instances {
	return Instances{runtime: runtime, locks: locks}
}

func (instances Instances) lock(value string) (domain.Name, func(), error) {
	name, err := domain.ParseName(value)
	if err != nil {
		return "", nil, err
	}
	release, err := instances.locks.Acquire("container-" + name.String())
	if err != nil {
		return "", nil, fmt.Errorf("lock container %q: %w", name, err)
	}
	return name, release, nil
}

func (instances Instances) Create(value string, group *string) error {
	if group != nil {
		if err := domain.ValidateGroup(*group); err != nil {
			return err
		}
	}
	name, release, err := instances.lock(value)
	if err != nil {
		return err
	}
	defer release()
	record, err := instances.runtime.Inspect(name)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("inspect container %q: %w", name, err)
	}
	if err == nil && record.PendingOperation != "create" {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, name)
	}
	selectedGroup := record.Group
	if group != nil {
		if err == nil && record.Group != *group {
			return fmt.Errorf("pending creation has a different group; retry with its original group")
		}
		selectedGroup = *group
	}
	// Port reservation lasts through Incus init/device configuration. Once the
	// metadata exists, other allocators can see it even if preparation fails.
	allocationDone, err := instances.locks.Acquire("allocation")
	if err != nil {
		return err
	}
	port := record.VNCPort
	if port == 0 {
		port, err = instances.runtime.AllocateVNCPort()
		if err != nil {
			allocationDone()
			return fmt.Errorf("allocate VNC port: %w", err)
		}
	}
	err = instances.runtime.Create(name, port, selectedGroup)
	allocationDone()
	if err != nil {
		return fmt.Errorf("create container %q (retry create %s): %w", name, name, err)
	}
	instance := domain.Instance{Name: name, Group: selectedGroup, VNCPort: port, DesiredState: domain.DesiredStopped}
	if record.VNCPort != 0 {
		instance.DesiredState = record.DesiredState
	}
	if err := instances.ready(domain.InstanceStatus{Instance: instance, PendingOperation: "create"}); err != nil {
		return fmt.Errorf("container %q remains incomplete; retry create %s: %w", name, name, err)
	}
	return nil
}

func (instances Instances) List() ([]domain.InstanceStatus, error) {
	records, err := instances.runtime.List()
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	names := make([]domain.Name, 0, len(records))
	for _, record := range records {
		names = append(names, record.Name)
	}
	// Disk accounting is optional. Unknown usage must not hide lifecycle state.
	usage, _ := instances.runtime.DiskUsage(names)
	for index := range records {
		records[index].DiskUsageBytes, records[index].DiskUsageKnown = usage[records[index].Name]
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, nil
}

func (instances Instances) Start(value string) error {
	_, err := instances.start(value)
	return err
}

// start releases its lock before SSH or a VNC viewer begins. Two sessions can
// connect independently while stop/remove still serialize their preparation.
func (instances Instances) start(value string) (domain.Instance, error) {
	name, release, err := instances.lock(value)
	if err != nil {
		return domain.Instance{}, err
	}
	defer release()
	record, err := instances.runtime.Inspect(name)
	if err != nil {
		return domain.Instance{}, fmt.Errorf("inspect container %q: %w", name, err)
	}
	if record.PendingOperation == "rename" {
		return domain.Instance{}, fmt.Errorf("container %q has a pending rename; retry rename %s %s", name, record.RenameFrom, record.RenameTo)
	}
	if record.RuntimeStatus != domain.StatusRunning && record.RuntimeStatus != domain.StatusStopped {
		return domain.Instance{}, fmt.Errorf("cannot start container %q in state %q", name, record.RuntimeStatus)
	}
	if record.PendingOperation == "create" {
		if err := instances.runtime.Create(name, record.VNCPort, record.Group); err != nil {
			return domain.Instance{}, err
		}
	}
	if err := instances.ready(record); err != nil {
		return domain.Instance{}, err
	}
	return record.Instance, nil
}

// A creation checkpoint is cleared only after all required preparation succeeds.
// Both a failed create and a successfully imported but unstarted archive can be
// completed with start; no local JSON commit needs to accompany Incus changes.
func (instances Instances) ready(instance domain.InstanceStatus) error {
	name := instance.Name
	if err := instances.runtime.Start(name); err != nil {
		return fmt.Errorf("start container %q: %w", name, err)
	}
	if err := instances.runtime.PrepareSSH(name, instance.VNCPort); err != nil {
		return fmt.Errorf("prepare SSH for container %q: %w", name, err)
	}
	if instance.DesiredState != domain.DesiredRunning {
		if err := instances.runtime.EnableAutostart(name); err != nil {
			return fmt.Errorf("enable automatic restart for container %q: %w", name, err)
		}
	}
	if instance.PendingOperation != "" {
		if err := instances.runtime.Complete(name); err != nil {
			return fmt.Errorf("finish preparation for container %q: %w", name, err)
		}
	}
	return nil
}

func (instances Instances) Stop(value string) error {
	name, release, err := instances.lock(value)
	if err != nil {
		return err
	}
	defer release()
	record, err := instances.runtime.Inspect(name)
	if err != nil {
		return fmt.Errorf("inspect container %q: %w", name, err)
	}
	if record.PendingOperation == "rename" {
		return fmt.Errorf("container %q has a pending rename; retry rename %s %s", name, record.RenameFrom, record.RenameTo)
	}
	if record.RuntimeStatus != domain.StatusRunning && record.RuntimeStatus != domain.StatusStopped {
		return fmt.Errorf("cannot stop container %q in state %q", name, record.RuntimeStatus)
	}
	// Persist stop intent first so an interrupted stop cannot resurrect the
	// container after a host reboot. Retrying stop completes the same request.
	if err := instances.runtime.DisableAutostart(name); err != nil {
		return fmt.Errorf("disable automatic restart for container %q: %w", name, err)
	}
	if err := instances.runtime.Stop(name); err != nil {
		return fmt.Errorf("stop container %q (retry stop %s): %w", name, name, err)
	}
	return nil
}

func (instances Instances) SSH(value string) error {
	instance, err := instances.start(value)
	if err != nil {
		return err
	}
	if err := instances.runtime.SSH(instance.Name, instance.VNCPort); err != nil {
		var remoteExit interface{ ExitCode() int }
		if errors.As(err, &remoteExit) {
			code := remoteExit.ExitCode()
			if code >= 0 && code != 255 {
				return shellExitError{code: code}
			}
		}
		return fmt.Errorf("open shell in instance %q: %w", instance.Name, err)
	}
	return nil
}
func (instances Instances) VNC(value string) error {
	instance, err := instances.start(value)
	if err != nil {
		return err
	}
	if err := instances.runtime.OpenVNC(instance.VNCPort); err != nil {
		return fmt.Errorf("open VNC for container %q: %w", instance.Name, err)
	}
	return nil
}
func (instances Instances) Remove(value string, force bool) error {
	name, release, err := instances.lock(value)
	if err != nil {
		return err
	}
	defer release()
	record, err := instances.runtime.Inspect(name)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("inspect container %q: %w", name, err)
	}
	if err == nil {
		if record.PendingOperation == "rename" {
			return fmt.Errorf("container %q has a pending rename; retry rename %s %s", name, record.RenameFrom, record.RenameTo)
		}
		if record.RuntimeStatus == domain.StatusRunning && !force {
			return fmt.Errorf("%w: stop %s before removing it", ErrInstanceRunning, name)
		}
		if record.RuntimeStatus != domain.StatusRunning && record.RuntimeStatus != domain.StatusStopped {
			return fmt.Errorf("container %q is not safely stopped", name)
		}
		if err := instances.runtime.DisableAutostart(name); err != nil {
			return fmt.Errorf("disable automatic restart for container %q: %w", name, err)
		}
		if record.RuntimeStatus == domain.StatusRunning {
			if err := instances.runtime.Stop(name); err != nil {
				return fmt.Errorf("stop container %q before removing it: %w", name, err)
			}
		}
	}
	// Delete also handles already absent containers, so interrupted local key
	// cleanup is retryable. Unavailable/unowned runtime is never treated as absent.
	if err := instances.runtime.Delete(name); err != nil {
		return fmt.Errorf("remove container %q: %w", name, err)
	}
	return nil
}
func (instances Instances) Update() error {
	if err := instances.runtime.UpdateImage(); err != nil {
		return fmt.Errorf("update managed desktop image: %w", err)
	}
	records, err := instances.runtime.List()
	if err != nil {
		return fmt.Errorf("list containers for update: %w", err)
	}
	for _, listed := range records {
		name, release, err := instances.lock(listed.Name.String())
		if err != nil {
			return err
		}
		// Re-read after acquiring the lock: another process may have stopped or
		// removed this container since the initial inventory snapshot.
		record, err := instances.runtime.Inspect(name)
		if errors.Is(err, domain.ErrNotFound) {
			release()
			continue
		}
		if err != nil {
			release()
			return err
		}
		if record.PendingOperation != "" || (record.RuntimeStatus != domain.StatusRunning && record.RuntimeStatus != domain.StatusStopped) {
			release()
			return fmt.Errorf("container %q is not ready for update", name)
		}
		wasStopped := record.RuntimeStatus == domain.StatusStopped
		if wasStopped {
			if err := instances.runtime.Start(name); err != nil {
				release()
				return fmt.Errorf("temporarily start container %q: %w", name, err)
			}
		}
		updateErr := instances.runtime.UpdateGuest(name)
		if wasStopped {
			if err := instances.runtime.Stop(name); err != nil {
				updateErr = errors.Join(updateErr, fmt.Errorf("restore stopped state for container %q: %w", name, err))
			}
		}
		release()
		if updateErr != nil {
			return fmt.Errorf("update container %q: %w", name, updateErr)
		}
	}
	return nil
}

func (instances Instances) SetGroup(value, group string) error {
	if err := domain.ValidateGroup(group); err != nil {
		return err
	}
	name, release, err := instances.lock(value)
	if err != nil {
		return err
	}
	defer release()
	return instances.runtime.SetGroup(name, group)
}

func (instances Instances) Rename(oldValue, newValue string) error {
	oldName, err := domain.ParseName(oldValue)
	if err != nil {
		return err
	}
	newName, err := domain.ParseName(newValue)
	if err != nil {
		return err
	}
	if oldName == newName {
		return fmt.Errorf("new name must differ from the current name")
	}
	// 반대 방향의 이름 변경도 같은 순서로 잠금을 얻어 교착을 피합니다.
	names := []string{oldValue, newValue}
	sort.Strings(names)
	for _, value := range names {
		_, release, err := instances.lock(value)
		if err != nil {
			return err
		}
		defer release()
	}
	if err := instances.runtime.Rename(oldName, newName); err != nil {
		return fmt.Errorf("rename %s %s failed; retry the same command after resolving the error: %w", oldName, newName, err)
	}
	return nil
}
