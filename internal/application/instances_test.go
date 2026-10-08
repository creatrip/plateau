package application_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/creatrip/plateau/internal/application"
	"github.com/creatrip/plateau/internal/domain"
)

type runtimeFake struct {
	statuses     map[domain.Name]domain.RuntimeStatus
	instances    map[domain.Name]domain.Instance
	pending      map[domain.Name]string
	faults       map[string]error
	diskUsage    map[domain.Name]uint64
	diskUsageErr error
	autostart    map[domain.Name]bool
	allocated    uint16
	sshCount     int
	sshVNC       uint16
	sshError     error
	onRename     func()
	onSSH        func()
	preparedName domain.Name
	preparedVNC  uint16
	vncPort      uint16
	createdName  domain.Name
	backup       domain.Instance
	restored     domain.Instance
	manifest     domain.Instance
	updated      []domain.Name
	imageUpdates int
}

func newRuntimeFake() *runtimeFake {
	return &runtimeFake{instances: map[domain.Name]domain.Instance{}, pending: map[domain.Name]string{}, faults: map[string]error{}, statuses: map[domain.Name]domain.RuntimeStatus{}, diskUsage: map[domain.Name]uint64{}, autostart: map[domain.Name]bool{}, allocated: 32001}
}

func (runtime *runtimeFake) AllocateVNCPort() (uint16, error) {
	return runtime.allocated, nil
}

func (runtime *runtimeFake) Create(name domain.Name, port uint16, group string) error {
	if err := runtime.faults["create"]; err != nil {
		return err
	}
	runtime.createdName = name
	if _, exists := runtime.instances[name]; !exists {
		runtime.instances[name] = domain.Instance{Name: name, Group: group, VNCPort: port, DesiredState: domain.DesiredStopped}
		runtime.statuses[name] = domain.StatusStopped
		runtime.autostart[name] = false
		runtime.pending[name] = "create"
	}
	return nil
}

func (runtime *runtimeFake) DiskUsage(_ []domain.Name) (map[domain.Name]uint64, error) {
	return runtime.diskUsage, runtime.diskUsageErr
}

func (runtime *runtimeFake) Start(name domain.Name) error {
	if err := runtime.faults["start"]; err != nil {
		return err
	}
	runtime.statuses[name] = domain.StatusRunning
	return nil
}

func (runtime *runtimeFake) Stop(name domain.Name) error {
	if err := runtime.faults["stop"]; err != nil {
		return err
	}
	runtime.statuses[name] = domain.StatusStopped
	return nil
}

func (runtime *runtimeFake) SSH(_ domain.Name, vncPort uint16) error {
	if runtime.onSSH != nil {
		runtime.onSSH()
	}
	runtime.sshCount++
	runtime.sshVNC = vncPort
	return runtime.sshError
}

func (runtime *runtimeFake) PrepareSSH(name domain.Name, vncPort uint16) error {
	if err := runtime.faults["prepare"]; err != nil {
		return err
	}
	runtime.preparedName = name
	runtime.preparedVNC = vncPort
	return nil
}

type remoteExitError struct {
	code int
}

func (exit remoteExitError) Error() string {
	return "exit status"
}

func (exit remoteExitError) ExitCode() int {
	return exit.code
}

func (runtime *runtimeFake) OpenVNC(port uint16) error {
	runtime.vncPort = port
	return nil
}

func (runtime *runtimeFake) Delete(name domain.Name) error {
	if err := runtime.faults["delete"]; err != nil {
		return err
	}
	delete(runtime.statuses, name)
	delete(runtime.instances, name)
	return nil
}

func (runtime *runtimeFake) EnableAutostart(name domain.Name) error {
	if err := runtime.faults["autostart"]; err != nil {
		return err
	}
	runtime.autostart[name] = true
	record := runtime.instances[name]
	record.DesiredState = domain.DesiredRunning
	runtime.instances[name] = record
	return nil
}

func (runtime *runtimeFake) DisableAutostart(name domain.Name) error {
	if err := runtime.faults["autostart"]; err != nil {
		return err
	}
	runtime.autostart[name] = false
	record := runtime.instances[name]
	record.DesiredState = domain.DesiredStopped
	runtime.instances[name] = record
	return nil
}

func (runtime *runtimeFake) Backup(instance domain.Instance, destination string) error {
	if err := runtime.faults["backup"]; err != nil {
		return err
	}
	runtime.backup = instance
	return os.WriteFile(destination, []byte("bundle"), 0o600)
}

func (runtime *runtimeFake) ReadBackupManifest(_ string) (domain.Instance, error) {
	return runtime.manifest, nil
}

func (runtime *runtimeFake) RestoreBackup(_ string, instance domain.Instance) error {
	runtime.instances[instance.Name] = instance
	runtime.autostart[instance.Name] = instance.DesiredState == domain.DesiredRunning
	runtime.pending[instance.Name] = "restore"
	runtime.restored = instance
	runtime.statuses[instance.Name] = domain.StatusStopped
	return nil
}

func TestRestoreAllocatesPortsForTheDestinationHost(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.manifest = domain.Instance{Name: "restored", VNCPort: 30000, DesiredState: domain.DesiredRunning}
	runtime.allocated = 30003
	service := application.NewInstances(runtime, &lockFake{})
	if _, err := service.Restore("backup.plateau"); err != nil {
		t.Fatal(err)
	}
	if runtime.restored.VNCPort != 30003 || runtime.instances["restored"].VNCPort != 30003 || runtime.preparedVNC != 30003 {
		t.Fatalf("restore=%+v record=%+v prepared port=%d", runtime.restored, runtime.instances["restored"], runtime.preparedVNC)
	}
}

func (runtime *runtimeFake) UpdateGuest(name domain.Name) error {
	if err := runtime.faults["update"]; err != nil {
		return err
	}
	runtime.updated = append(runtime.updated, name)
	return nil
}

func (runtime *runtimeFake) UpdateImage() error {
	runtime.imageUpdates++
	return nil
}

func TestUnsafeRuntimeStateDoesNotChangeRestartIntent(t *testing.T) {
	for _, operation := range []string{"stop", "backup"} {
		t.Run(operation, func(t *testing.T) {
			runtime := newRuntimeFake()
			runtime.statuses["a"] = domain.StatusBroken
			runtime.autostart["a"] = true
			runtime.instances["a"] = domain.Instance{Name: "a", VNCPort: 32001, DesiredState: domain.DesiredRunning}
			service := application.NewInstances(runtime, &lockFake{})
			var err error
			if operation == "stop" {
				err = service.Stop("a")
			} else {
				t.Chdir(t.TempDir())
				_, err = service.Backup("a")
			}
			if err == nil || !runtime.autostart["a"] || runtime.instances["a"].DesiredState != domain.DesiredRunning || runtime.backup.Name != "" {
				t.Fatalf("err=%v autostart=%v record=%+v backup=%+v", err, runtime.autostart, runtime.instances["a"], runtime.backup)
			}
		})
	}
}

type lockFake struct {
	held         map[string]bool
	acquired     []string
	failResource string
}

func (locks *lockFake) Acquire(resource string) (func(), error) {
	if locks.failResource == resource {
		return nil, errors.New("lock interrupted")
	}
	locks.acquired = append(locks.acquired, resource)
	if locks.held == nil {
		locks.held = map[string]bool{}
	}
	if locks.held[resource] {
		return nil, errors.New("recursive lock")
	}
	locks.held[resource] = true
	return func() { delete(locks.held, resource) }, nil
}
func (runtime *runtimeFake) Inspect(name domain.Name) (domain.InstanceStatus, error) {
	if err := runtime.faults["inspect"]; err != nil {
		return domain.InstanceStatus{}, err
	}
	instance, exists := runtime.instances[name]
	if !exists {
		return domain.InstanceStatus{}, domain.ErrNotFound
	}
	status := runtime.statuses[name]
	if status == "" {
		status = domain.StatusStopped
	}
	return domain.InstanceStatus{Instance: instance, RuntimeStatus: status, PendingOperation: runtime.pending[name]}, nil
}
func (runtime *runtimeFake) List() ([]domain.InstanceStatus, error) {
	records := []domain.InstanceStatus{}
	for name := range runtime.instances {
		record, err := runtime.Inspect(name)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, nil
}
func (runtime *runtimeFake) Complete(name domain.Name) error {
	if err := runtime.faults["complete"]; err != nil {
		return err
	}
	delete(runtime.pending, name)
	return nil
}

func TestCreateBuildsRunningRecoverableInstance(t *testing.T) {
	runtime := newRuntimeFake()
	instances := application.NewInstances(runtime, &lockFake{})

	if err := instances.Create("pg-a", nil); err != nil {
		t.Fatalf("create pg-a: %v", err)
	}
	record := runtime.instances["pg-a"]
	if !reflect.DeepEqual(record, domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}) {
		t.Fatalf("record = %+v, want running instance", record)
	}
	if runtime.createdName != "pg-a" || runtime.statuses["pg-a"] != domain.StatusRunning || !runtime.autostart["pg-a"] || runtime.preparedName != "pg-a" || runtime.preparedVNC != 32001 {
		t.Fatalf("runtime = %+v, want created, running, autostart instance", runtime)
	}
}

func TestCreateRejectsInvalidOrExistingName(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredStopped}
	instances := application.NewInstances(runtime, &lockFake{})

	if err := instances.Create("PG-A", nil); err == nil {
		t.Fatal("create invalid name succeeded, want error")
	}
	if err := instances.Create("pg-a", nil); !errors.Is(err, application.ErrAlreadyExists) {
		t.Fatalf("create existing name error = %v, want ErrAlreadyExists", err)
	}
	if runtime.createdName != "" {
		t.Fatalf("created runtime %q for rejected request", runtime.createdName)
	}
}

func TestListCombinesRecordsWithRuntimeStatus(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}
	runtime.instances["worker"] = domain.Instance{Name: "worker", VNCPort: 32002, DesiredState: domain.DesiredStopped}
	runtime.statuses["pg-a"] = domain.StatusRunning
	runtime.statuses["worker"] = domain.StatusStopped
	runtime.diskUsage["pg-a"] = 16 << 20
	runtime.diskUsage["worker"] = 1536
	instances := application.NewInstances(runtime, &lockFake{})

	statuses, err := instances.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(statuses) != 2 || statuses[0].Name != "pg-a" || statuses[0].RuntimeStatus != domain.StatusRunning || !statuses[0].DiskUsageKnown || statuses[0].DiskUsageBytes != 16<<20 || statuses[1].Name != "worker" || statuses[1].RuntimeStatus != domain.StatusStopped || !statuses[1].DiskUsageKnown || statuses[1].DiskUsageBytes != 1536 {
		t.Fatalf("statuses = %+v, want sorted runtime states", statuses)
	}
}

func TestListKeepsStatusesWhenDiskUsageIsUnavailable(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.diskUsageErr = errors.New("usage unavailable")
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}
	runtime.statuses["pg-a"] = domain.StatusRunning
	instances := application.NewInstances(runtime, &lockFake{})

	statuses, err := instances.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(statuses) != 1 || statuses[0].RuntimeStatus != domain.StatusRunning || statuses[0].DiskUsageKnown {
		t.Fatalf("statuses = %+v, want running status with unknown disk usage", statuses)
	}
}

func TestStartAndStopPersistRestartIntent(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredStopped}
	runtime.statuses["pg-a"] = domain.StatusStopped
	instances := application.NewInstances(runtime, &lockFake{})

	if err := instances.Start("pg-a"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if runtime.instances["pg-a"].DesiredState != domain.DesiredRunning || !runtime.autostart["pg-a"] || runtime.statuses["pg-a"] != domain.StatusRunning {
		t.Fatalf("start state = record %+v, runtime %+v", runtime.instances["pg-a"], runtime)
	}
	if err := instances.Stop("pg-a"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if runtime.instances["pg-a"].DesiredState != domain.DesiredStopped || runtime.autostart["pg-a"] || runtime.statuses["pg-a"] != domain.StatusStopped {
		t.Fatalf("stop state = record %+v, runtime %+v", runtime.instances["pg-a"], runtime)
	}
}

func TestSSHStartsStoppedInstanceBeforeConnecting(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredStopped}
	runtime.statuses["pg-a"] = domain.StatusStopped
	instances := application.NewInstances(runtime, &lockFake{})

	if err := instances.SSH("pg-a"); err != nil {
		t.Fatalf("ssh: %v", err)
	}
	if runtime.sshCount != 1 || runtime.sshVNC != 32001 || runtime.statuses["pg-a"] != domain.StatusRunning || runtime.instances["pg-a"].DesiredState != domain.DesiredRunning {
		t.Fatalf("ssh state = count %d, record %+v, runtime %+v", runtime.sshCount, runtime.instances["pg-a"], runtime)
	}
}

func TestSSHPreservesRemoteShellExitStatus(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.sshError = remoteExitError{code: 130}
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}
	runtime.statuses["pg-a"] = domain.StatusRunning
	instances := application.NewInstances(runtime, &lockFake{})

	err := instances.SSH("pg-a")
	var shellExit interface{ ShellExitCode() int }
	if !errors.As(err, &shellExit) || shellExit.ShellExitCode() != 130 {
		t.Fatalf("SSH error = %v, want silent remote shell exit 130", err)
	}
}

func TestSSHReportsConnectionFailure(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.sshError = remoteExitError{code: 255}
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}
	runtime.statuses["pg-a"] = domain.StatusRunning
	instances := application.NewInstances(runtime, &lockFake{})

	err := instances.SSH("pg-a")
	var shellExit interface{ ShellExitCode() int }
	if err == nil || errors.As(err, &shellExit) || !strings.Contains(err.Error(), "open shell in instance") {
		t.Fatalf("SSH error = %v, want visible connection failure", err)
	}
}

func TestVNCStartsStoppedInstanceBeforeOpeningDesktop(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredStopped}
	runtime.statuses["pg-a"] = domain.StatusStopped
	instances := application.NewInstances(runtime, &lockFake{})

	if err := instances.VNC("pg-a"); err != nil {
		t.Fatalf("vnc: %v", err)
	}
	if runtime.vncPort != 32001 || runtime.statuses["pg-a"] != domain.StatusRunning || runtime.instances["pg-a"].DesiredState != domain.DesiredRunning {
		t.Fatalf("vnc state = port %d, record %+v, runtime %+v", runtime.vncPort, runtime.instances["pg-a"], runtime)
	}
}

func TestRemoveRequiresStoppedInstance(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}
	runtime.statuses["pg-a"] = domain.StatusRunning
	instances := application.NewInstances(runtime, &lockFake{})

	if err := instances.Remove("pg-a", false); !errors.Is(err, application.ErrInstanceRunning) {
		t.Fatalf("remove running error = %v, want ErrInstanceRunning", err)
	}
	if err := instances.Stop("pg-a"); err != nil {
		t.Fatal(err)
	}
	if err := instances.Remove("pg-a", false); err != nil {
		t.Fatalf("remove stopped: %v", err)
	}
	if _, exists := runtime.instances["pg-a"]; exists {
		t.Fatal("instance record still exists after remove")
	}
	if _, exists := runtime.statuses["pg-a"]; exists {
		t.Fatal("runtime still exists after remove")
	}
}

func TestForceRemoveStopsAndDeletesRunningInstance(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}
	runtime.statuses["pg-a"] = domain.StatusRunning
	runtime.autostart["pg-a"] = true
	instances := application.NewInstances(runtime, &lockFake{})

	if err := instances.Remove("pg-a", true); err != nil {
		t.Fatalf("force remove running instance: %v", err)
	}
	if _, exists := runtime.instances["pg-a"]; exists {
		t.Fatal("instance record still exists after force remove")
	}
	if _, exists := runtime.statuses["pg-a"]; exists {
		t.Fatal("runtime still exists after force remove")
	}
	if runtime.autostart["pg-a"] {
		t.Fatal("automatic restart remained enabled after force remove")
	}
}

func TestRemoveRejectsBrokenInstance(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredStopped}
	runtime.statuses["pg-a"] = domain.StatusBroken
	instances := application.NewInstances(runtime, &lockFake{})

	if err := instances.Remove("pg-a", false); err == nil {
		t.Fatal("remove broken instance succeeded")
	}
	if _, exists := runtime.instances["pg-a"]; !exists {
		t.Fatal("broken instance record was removed")
	}
}

func TestBackupTemporarilyStopsAndRestartsRunningInstance(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}
	runtime.statuses["pg-a"] = domain.StatusRunning
	instances := application.NewInstances(runtime, &lockFake{})
	t.Chdir(t.TempDir())

	backupPath, err := instances.Backup("pg-a")
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if filepath.Ext(backupPath) != ".plateau" || !strings.Contains(filepath.Base(backupPath), "pg-a-") {
		t.Fatalf("backup path = %q", backupPath)
	}
	if runtime.backup.Name != "pg-a" || runtime.statuses["pg-a"] != domain.StatusRunning || runtime.instances["pg-a"].DesiredState != domain.DesiredRunning {
		t.Fatalf("backup state = backup %+v, runtime %+v", runtime.backup, runtime)
	}
}

func TestRestoreRecreatesOriginalNameAndDesiredState(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.manifest = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}
	instances := application.NewInstances(runtime, &lockFake{})

	name, err := instances.Restore("pg-a.plateau")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if name != "pg-a" || runtime.restored.Name != "pg-a" || runtime.statuses["pg-a"] != domain.StatusRunning || !runtime.autostart["pg-a"] {
		t.Fatalf("restore state = name %q, restored %+v, runtime %+v", name, runtime.restored, runtime)
	}
	if runtime.instances["pg-a"].DesiredState != domain.DesiredRunning {
		t.Fatalf("record = %+v", runtime.instances["pg-a"])
	}
}

func TestRestoreRejectsNameCollisionBeforeWritingRuntime(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.manifest = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredStopped}
	runtime.instances["pg-a"] = runtime.manifest
	instances := application.NewInstances(runtime, &lockFake{})

	if _, err := instances.Restore("pg-a.plateau"); !errors.Is(err, application.ErrAlreadyExists) {
		t.Fatalf("restore collision error = %v, want ErrAlreadyExists", err)
	}
	if runtime.restored.Name != "" {
		t.Fatalf("restored runtime despite collision: %+v", runtime.restored)
	}
}

func TestUpdatePreservesRunningAndStoppedStates(t *testing.T) {
	runtime := newRuntimeFake()
	runtime.instances["pg-a"] = domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}
	runtime.instances["worker"] = domain.Instance{Name: "worker", VNCPort: 32002, DesiredState: domain.DesiredStopped}
	runtime.statuses["pg-a"] = domain.StatusRunning
	runtime.statuses["worker"] = domain.StatusStopped
	instances := application.NewInstances(runtime, &lockFake{})

	if err := instances.Update(); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !reflect.DeepEqual(runtime.updated, []domain.Name{"pg-a", "worker"}) {
		t.Fatalf("updated = %v", runtime.updated)
	}
	if runtime.imageUpdates != 1 {
		t.Fatalf("image updates = %d, want 1", runtime.imageUpdates)
	}
	if runtime.statuses["pg-a"] != domain.StatusRunning || runtime.statuses["worker"] != domain.StatusStopped {
		t.Fatalf("statuses = %v", runtime.statuses)
	}
}

func TestCreateRetriesOnlyIncompleteOwnedContainers(t *testing.T) {
	for _, phase := range []string{"start", "prepare", "autostart", "complete"} {
		t.Run(phase, func(t *testing.T) {
			runtime := newRuntimeFake()
			service := application.NewInstances(runtime, &lockFake{})
			failure := errors.New("injected " + phase)
			runtime.faults[phase] = failure
			if err := service.Create("a", nil); !errors.Is(err, failure) {
				t.Fatalf("err=%v", err)
			}
			if runtime.pending["a"] != "create" {
				t.Fatal("failed creation lost its retry checkpoint")
			}
			delete(runtime.faults, phase)
			if err := service.Create("a", nil); err != nil {
				t.Fatal(err)
			}
			if runtime.pending["a"] != "" || runtime.statuses["a"] != domain.StatusRunning {
				t.Fatalf("incomplete retry: %+v", runtime)
			}
			if err := service.Create("a", nil); !errors.Is(err, application.ErrAlreadyExists) {
				t.Fatalf("completed duplicate: %v", err)
			}
		})
	}
}
func TestBackupRestartsAfterExportFailureAndReportsRestartFailure(t *testing.T) {
	runtime := newRuntimeFake()
	service := application.NewInstances(runtime, &lockFake{})
	if err := service.Create("a", nil); err != nil {
		t.Fatal(err)
	}
	exportFailure := errors.New("export failed")
	runtime.faults["backup"] = exportFailure
	if _, err := service.Backup("a"); !errors.Is(err, exportFailure) {
		t.Fatalf("err=%v", err)
	}
	if runtime.statuses["a"] != domain.StatusRunning || !runtime.autostart["a"] {
		t.Fatal("backup changed running intent")
	}
	restartFailure := errors.New("restart failed")
	runtime.faults["start"] = restartFailure
	if _, err := service.Backup("a"); !errors.Is(err, exportFailure) || !errors.Is(err, restartFailure) {
		t.Fatalf("lost failure: %v", err)
	}
}
func TestFailedUpdateRestoresStoppedState(t *testing.T) {
	runtime := newRuntimeFake()
	service := application.NewInstances(runtime, &lockFake{})
	if err := service.Create("a", nil); err != nil {
		t.Fatal(err)
	}
	if err := service.Stop("a"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("apt failed")
	runtime.faults["update"] = failure
	if err := service.Update(); !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
	if runtime.statuses["a"] != domain.StatusStopped || runtime.autostart["a"] {
		t.Fatal("update changed stopped state")
	}
}
func TestInspectionFailureNeverAuthorizesCreationOrRemoval(t *testing.T) {
	runtime := newRuntimeFake()
	service := application.NewInstances(runtime, &lockFake{})
	failure := errors.New("connection unavailable")
	runtime.faults["inspect"] = failure
	if err := service.Create("a", nil); !errors.Is(err, failure) {
		t.Fatalf("create err=%v", err)
	}
	if err := service.Remove("a", true); !errors.Is(err, failure) {
		t.Fatalf("remove err=%v", err)
	}
	if runtime.createdName != "" {
		t.Fatal("created after failed inspection")
	}
}
func TestRemoveRetryAfterDeletionIsSafe(t *testing.T) {
	runtime := newRuntimeFake()
	service := application.NewInstances(runtime, &lockFake{})
	if err := service.Create("a", nil); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove("a", true); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove("a", true); err != nil {
		t.Fatal(err)
	}
}

func TestInteractiveSSHDoesNotHoldLifecycleLocks(t *testing.T) {
	runtime := newRuntimeFake()
	locks := &lockFake{}
	service := application.NewInstances(runtime, locks)
	if err := service.Create("a", nil); err != nil {
		t.Fatal(err)
	}
	runtime.onSSH = func() {
		if len(locks.held) != 0 {
			t.Fatalf("interactive session retained locks: %v", locks.held)
		}
	}
	if err := service.SSH("a"); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRetriesPublishedIncompleteContainer(t *testing.T) {
	for _, state := range []domain.DesiredState{domain.DesiredRunning, domain.DesiredStopped} {
		for _, phase := range []string{"prepare", "autostart", "complete"} {
			if state == domain.DesiredStopped && phase == "prepare" {
				continue
			}
			t.Run(string(state)+"/"+phase, func(t *testing.T) {
				runtime := newRuntimeFake()
				runtime.manifest = domain.Instance{Name: "a", VNCPort: 30000, DesiredState: state}
				service := application.NewInstances(runtime, &lockFake{})
				failure := errors.New("interrupted " + phase)
				runtime.faults[phase] = failure
				if _, err := service.Restore("backup.plateau"); !errors.Is(err, failure) {
					t.Fatalf("err=%v", err)
				}
				delete(runtime.faults, phase)
				if _, err := service.Restore("backup.plateau"); err != nil {
					t.Fatal(err)
				}
				if runtime.pending["a"] != "" || runtime.instances["a"].DesiredState != state {
					t.Fatalf("incomplete restore: %+v", runtime)
				}
			})
		}
	}
}

func TestStopFailureKeepsIntentAndRetryCompletes(t *testing.T) {
	runtime := newRuntimeFake()
	service := application.NewInstances(runtime, &lockFake{})
	if err := service.Create("a", nil); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("stop interrupted")
	runtime.faults["stop"] = failure
	if err := service.Stop("a"); !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
	if runtime.instances["a"].DesiredState != domain.DesiredStopped || runtime.statuses["a"] != domain.StatusRunning {
		t.Fatal("lost persistent stop intent")
	}
	delete(runtime.faults, "stop")
	if err := service.Stop("a"); err != nil {
		t.Fatal(err)
	}
	if runtime.statuses["a"] != domain.StatusStopped {
		t.Fatal("retry did not stop container")
	}
}

func (runtime *runtimeFake) SetGroup(name domain.Name, group string) error {
	instance := runtime.instances[name]
	instance.Group = group
	runtime.instances[name] = instance
	return runtime.faults["group"]
}

func (runtime *runtimeFake) Rename(oldName, newName domain.Name) error {
	if runtime.onRename != nil {
		runtime.onRename()
	}
	return runtime.faults["rename"]
}
