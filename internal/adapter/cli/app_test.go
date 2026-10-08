package cli_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/creatrip/plateau/internal/adapter/cli"
	"github.com/creatrip/plateau/internal/application"
	"github.com/creatrip/plateau/internal/domain"
)

type lockStub struct{}

func (lockStub) Acquire(string) (func(), error) { return func() {}, nil }

type hostProvisionerStub struct {
	ensure func() error
	update func() error
}

func (provision hostProvisionerStub) Ensure() error {
	if provision.ensure == nil {
		return nil
	}
	return provision.ensure()
}

func (provision hostProvisionerStub) Update() error {
	if provision.update == nil {
		return nil
	}
	return provision.update()
}

type instanceServiceStub struct {
	group       *string
	calls       []string
	list        []domain.InstanceStatus
	ssh         error
	removeForce bool
	update      func() error
}

func TestInvalidNamesDoNotBootstrapDependencies(t *testing.T) {
	for _, operation := range []string{"create", "ssh", "vnc", "start", "stop", "rm", "backup"} {
		t.Run(operation, func(t *testing.T) {
			bootstrapped := false
			bootstrap := application.NewBootstrap(lockStub{}, hostProvisionerStub{ensure: func() error {
				bootstrapped = true
				return nil
			}})
			service := &instanceServiceStub{}
			var output bytes.Buffer
			app := cli.New("test", bootstrap, service, &output, &output)
			if code := app.Run([]string{operation, "../invalid"}); code == 0 || bootstrapped || len(service.calls) != 0 {
				t.Fatalf("code=%d bootstrap=%v calls=%v", code, bootstrapped, service.calls)
			}
		})
	}
}

func (service *instanceServiceStub) Create(name string, group *string) error {
	service.group = group
	service.calls = append(service.calls, "create "+name)
	return nil
}

func (service *instanceServiceStub) List() ([]domain.InstanceStatus, error) {
	service.calls = append(service.calls, "ls")
	return service.list, nil
}

func (service *instanceServiceStub) SSH(name string) error {
	service.calls = append(service.calls, "ssh "+name)
	return service.ssh
}

type shellExitStub struct {
	code int
}

func (exit shellExitStub) Error() string {
	return "remote shell exited"
}

func (exit shellExitStub) ShellExitCode() int {
	return exit.code
}

func (service *instanceServiceStub) VNC(name string) error {
	service.calls = append(service.calls, "vnc "+name)
	return nil
}

func (service *instanceServiceStub) Remove(name string, force bool) error {
	service.calls = append(service.calls, "rm "+name)
	service.removeForce = force
	return nil
}

func (service *instanceServiceStub) Stop(name string) error {
	service.calls = append(service.calls, "stop "+name)
	return nil
}

func (service *instanceServiceStub) Start(name string) error {
	service.calls = append(service.calls, "start "+name)
	return nil
}

func (service *instanceServiceStub) Backup(name string) (string, error) {
	service.calls = append(service.calls, "backup "+name)
	return "/tmp/" + name + ".plateau", nil
}

func (service *instanceServiceStub) Restore(path string) (domain.Name, error) {
	service.calls = append(service.calls, "restore "+path)
	return "pg-a", nil
}

func (service *instanceServiceStub) Update() error {
	service.calls = append(service.calls, "update")
	if service.update != nil {
		return service.update()
	}
	return nil
}

func newApp(version string, stdout, stderr *bytes.Buffer) cli.App {
	return cli.New(version, application.NewBootstrap(lockStub{}, hostProvisionerStub{}), &instanceServiceStub{}, stdout, stderr)
}

func TestRunWithoutArgumentsPrintsUsage(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := newApp("dev", &stdout, &stderr).Run(nil)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if !strings.Contains(stdout.String(), "plateau [command]") {
		t.Fatalf("stdout = %q, want usage", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Available Commands:") {
		t.Fatalf("stdout = %q, want Cobra command list", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Manage lightweight Incus Debian containers on macOS") || strings.Contains(stdout.String(), "environments") {
		t.Fatalf("stdout = %q, want container terminology", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty output", stderr.String())
	}
}

func TestRunVersionFlagPrintsBuildVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := newApp("1.2.3", &stdout, &stderr).Run([]string{"--version"})

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if stdout.String() != "plateau 1.2.3\n" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), "plateau 1.2.3\n")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty output", stderr.String())
	}
}

func TestRunVersionPrintsBuildVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := newApp("1.2.3", &stdout, &stderr).Run([]string{"version"})

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if stdout.String() != "plateau 1.2.3\n" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), "plateau 1.2.3\n")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty output", stderr.String())
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := newApp("dev", &stdout, &stderr).Run([]string{"unknown"})

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty output", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unknown command "unknown" for "plateau"`) {
		t.Fatalf("stderr = %q, want unknown command error", stderr.String())
	}
}

func TestRunRejectsVersionArguments(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := newApp("dev", &stdout, &stderr).Run([]string{"version", "extra"})

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty output", stdout.String())
	}
	if !strings.Contains(stderr.String(), "version does not accept arguments") {
		t.Fatalf("stderr = %q, want invalid argument error", stderr.String())
	}
}

func TestRunHelpAndVersionDoNotBootstrapHost(t *testing.T) {
	for _, arguments := range [][]string{nil, {"--help"}, {"--version"}, {"version"}} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		ensureCount := 0
		app := cli.New("dev", application.NewBootstrap(lockStub{}, hostProvisionerStub{ensure: func() error {
			ensureCount++
			return nil
		}}), &instanceServiceStub{}, &stdout, &stderr)

		exitCode := app.Run(arguments)

		if exitCode != 0 {
			t.Fatalf("arguments = %v, exit code = %d, want 0", arguments, exitCode)
		}
		if ensureCount != 0 {
			t.Fatalf("arguments = %v, ensure count = %d, want 0", arguments, ensureCount)
		}
	}
}

func TestRunEnsuresHostDependenciesBeforeOperationalCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	ensureCount := 0
	app := cli.New("dev", application.NewBootstrap(lockStub{}, hostProvisionerStub{ensure: func() error {
		ensureCount++
		return nil
	}}), &instanceServiceStub{}, &stdout, &stderr)

	exitCode := app.Run([]string{"ls"})

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if ensureCount != 1 {
		t.Fatalf("ensure count = %d, want 1", ensureCount)
	}
}

func TestRunStopsWhenHostDependencyInstallationFails(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := cli.New("dev", application.NewBootstrap(lockStub{}, hostProvisionerStub{ensure: func() error {
		return errors.New("install lima")
	}}), &instanceServiceStub{}, &stdout, &stderr)

	exitCode := app.Run([]string{"ls"})

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty output", stdout.String())
	}
	if stderr.String() != "bootstrap: install lima\n" {
		t.Fatalf("stderr = %q, want bootstrap error", stderr.String())
	}
}

func TestRunUpdateOnlyUsesStableUpdatePath(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	ensureCount := 0
	updateCount := 0
	service := &instanceServiceStub{}
	app := cli.New("dev", application.NewBootstrap(lockStub{}, hostProvisionerStub{
		ensure: func() error {
			ensureCount++
			return nil
		},
		update: func() error {
			updateCount++
			return nil
		},
	}), service, &stdout, &stderr)

	exitCode := app.Run([]string{"update"})

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if ensureCount != 0 || updateCount != 1 {
		t.Fatalf("ensure count = %d, update count = %d, want 0 and 1", ensureCount, updateCount)
	}
	if !reflect.DeepEqual(service.calls, []string{"update"}) {
		t.Fatalf("instance calls = %v, want update", service.calls)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty output", stderr.String())
	}
}

func TestRunUpdateReportsFailure(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := cli.New("dev", application.NewBootstrap(lockStub{}, hostProvisionerStub{update: func() error {
		return errors.New("update lima")
	}}), &instanceServiceStub{}, &stdout, &stderr)

	exitCode := app.Run([]string{"update"})

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "update lima") {
		t.Fatalf("stderr = %q, want update error", stderr.String())
	}
}

func TestRunLifecycleCommandsUseOneRequiredName(t *testing.T) {
	for _, test := range []struct {
		command string
		call    string
	}{
		{command: "create", call: "create pg-a"},
		{command: "ssh", call: "ssh pg-a"},
		{command: "vnc", call: "vnc pg-a"},
		{command: "rm", call: "rm pg-a"},
		{command: "stop", call: "stop pg-a"},
		{command: "start", call: "start pg-a"},
	} {
		t.Run(test.command, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			service := &instanceServiceStub{}
			app := cli.New("dev", application.NewBootstrap(lockStub{}, hostProvisionerStub{}), service, &stdout, &stderr)

			if exitCode := app.Run([]string{test.command, "pg-a"}); exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			if len(service.calls) != 1 || service.calls[0] != test.call {
				t.Fatalf("calls = %v, want %q", service.calls, test.call)
			}
			if exitCode := app.Run([]string{test.command}); exitCode != 2 {
				t.Fatalf("missing name exit code = %d, want 2", exitCode)
			}
		})
	}
}

func TestRunRemoveAcceptsForceFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	service := &instanceServiceStub{}
	app := cli.New("dev", application.NewBootstrap(lockStub{}, hostProvisionerStub{}), service, &stdout, &stderr)

	if exitCode := app.Run([]string{"rm", "-f", "pg-a"}); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if !service.removeForce {
		t.Fatal("force remove flag was not passed to the instance service")
	}
}

func TestRunSSHReturnsRemoteShellStatusWithoutErrorOutput(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	service := &instanceServiceStub{ssh: shellExitStub{code: 130}}
	app := cli.New("dev", application.NewBootstrap(lockStub{}, hostProvisionerStub{}), service, &stdout, &stderr)

	if exitCode := app.Run([]string{"ssh", "pg-a"}); exitCode != 130 {
		t.Fatalf("exit code = %d, want remote shell status 130", exitCode)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no error output for remote shell exit", stderr.String())
	}
}

func TestRunListPrintsDockerStyleContainerStatus(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	service := &instanceServiceStub{list: []domain.InstanceStatus{
		{Instance: domain.Instance{Name: "failed", VNCPort: 32003, DesiredState: domain.DesiredRunning}, RuntimeStatus: domain.StatusBroken},
		{Instance: domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}, RuntimeStatus: domain.StatusRunning, DiskUsageBytes: 16 << 20, DiskUsageKnown: true},
		{Instance: domain.Instance{Name: "worker", VNCPort: 32002, DesiredState: domain.DesiredStopped}, RuntimeStatus: domain.StatusStopped, DiskUsageBytes: 1536, DiskUsageKnown: true},
	}}
	app := cli.New("dev", application.NewBootstrap(lockStub{}, hostProvisionerStub{}), service, &stdout, &stderr)

	if exitCode := app.Run([]string{"ls"}); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	want := "NAME    STATUS   DISK      GROUP\nfailed  broken   -         -\npg-a    running  16.0 MiB  -\nworker  stopped  1.5 KiB   -\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if exitCode := app.Run([]string{"ls", "extra"}); exitCode != 2 {
		t.Fatalf("ls argument exit code = %d, want 2", exitCode)
	}
}

func TestRunBackupAndRestorePrintTheirResults(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	service := &instanceServiceStub{}
	app := cli.New("dev", application.NewBootstrap(lockStub{}, hostProvisionerStub{}), service, &stdout, &stderr)

	if exitCode := app.Run([]string{"backup", "pg-a"}); exitCode != 0 {
		t.Fatalf("backup exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if stdout.String() != "/tmp/pg-a.plateau\n" {
		t.Fatalf("backup stdout = %q", stdout.String())
	}
	stdout.Reset()
	if exitCode := app.Run([]string{"restore", "saved.plateau"}); exitCode != 0 {
		t.Fatalf("restore exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if stdout.String() != "pg-a\n" {
		t.Fatalf("restore stdout = %q", stdout.String())
	}
	if !reflect.DeepEqual(service.calls, []string{"backup pg-a", "restore saved.plateau"}) {
		t.Fatalf("calls = %v", service.calls)
	}
}

func (service *instanceServiceStub) SetGroup(name, group string) error {
	service.group = &group
	service.calls = append(service.calls, "regroup "+name)
	return nil
}

func (service *instanceServiceStub) Rename(oldName, newName string) error {
	service.calls = append(service.calls, "rename "+oldName+" "+newName)
	return nil
}
