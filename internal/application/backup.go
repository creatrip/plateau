package application

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/creatrip/plateau/internal/domain"
)

// Backup orchestration owns temporary stop/restart policy; the runtime adapter
// owns the archive format and transport. Neither changes user restart intent.
func (instances Instances) Backup(value string) (string, error) {
	name, release, err := instances.lock(value)
	if err != nil {
		return "", err
	}
	defer release()
	record, err := instances.runtime.Inspect(name)
	if err != nil {
		return "", fmt.Errorf("load instance %q: %w", name, err)
	}
	instance := record.Instance
	status := record.RuntimeStatus
	if record.PendingOperation != "" || (status != domain.StatusRunning && status != domain.StatusStopped) {
		return "", fmt.Errorf("cannot back up instance %q in state %q", name, status)
	}
	wasRunning := status == domain.StatusRunning
	if wasRunning {
		if err := instances.runtime.Stop(name); err != nil {
			return "", fmt.Errorf("stop instance %q for backup: %w", name, err)
		}
	}
	destination, err := filepath.Abs(fmt.Sprintf("%s-%s.plateau", name, time.Now().Format("20060102-150405")))
	if err == nil {
		err = instances.runtime.Backup(instance, destination)
	}
	if wasRunning {
		if startErr := instances.runtime.Start(name); startErr != nil {
			err = errors.Join(err, fmt.Errorf("restart instance %q after backup: %w", name, startErr))
		}
	}
	if err != nil {
		return "", fmt.Errorf("back up instance %q: %w", name, err)
	}
	return destination, nil
}

func (instances Instances) Restore(path string) (domain.Name, error) {
	instance, err := instances.runtime.ReadBackupManifest(path)
	if err != nil {
		return "", fmt.Errorf("inspect backup: %w", err)
	}
	if err := instance.Validate(); err != nil {
		return "", fmt.Errorf("validate backup: %w", err)
	}
	_, release, err := instances.lock(instance.Name.String())
	if err != nil {
		return "", err
	}
	defer release()
	record, err := instances.runtime.Inspect(instance.Name)
	if err == nil && record.PendingOperation != "restore" {
		return "", fmt.Errorf("%w: %s", ErrAlreadyExists, instance.Name)
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return "", fmt.Errorf("inspect restore destination: %w", err)
	}
	allocationDone, err := instances.locks.Acquire("allocation")
	if err != nil {
		return "", err
	}
	port := record.VNCPort
	if port == 0 {
		port, err = instances.runtime.AllocateVNCPort()
		if err != nil {
			allocationDone()
			return "", fmt.Errorf("allocate restored container port: %w", err)
		}
	}
	instance.VNCPort = port
	err = instances.runtime.RestoreBackup(path, instance)
	allocationDone()
	if err != nil {
		return "", fmt.Errorf("restore container %q: %w", instance.Name, err)
	}
	if instance.DesiredState == domain.DesiredRunning {
		// Import publishes a stopped, non-autostarting checkpoint first.
		preparation := instance
		preparation.DesiredState = domain.DesiredStopped
		if err := instances.ready(domain.InstanceStatus{Instance: preparation, PendingOperation: "restore"}); err != nil {
			return instance.Name, fmt.Errorf("container %q was restored; run start %s to finish: %w", instance.Name, instance.Name, err)
		}
	} else {
		if err := instances.runtime.DisableAutostart(instance.Name); err != nil {
			return instance.Name, err
		}
		if err := instances.runtime.Complete(instance.Name); err != nil {
			return instance.Name, err
		}
	}
	return instance.Name, nil
}
