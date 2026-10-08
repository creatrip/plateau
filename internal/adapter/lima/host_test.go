package lima

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

type hostCommandCall struct {
	command string
	args    []string
	stdin   string
}

type probeExit int

func (exit probeExit) Error() string { return fmt.Sprintf("exit status %d", exit) }
func (exit probeExit) ExitCode() int { return int(exit) }

type hostRunnerFake struct {
	calls          []hostCommandCall
	mutations      []hostCommandCall
	output         []byte
	outputs        map[string][]byte
	listOutputs    [][]byte
	errors         map[string]error
	errorSequences map[string][]error
}

func TestSystemHostRunnerSeparatesSuccessfulStderrFromStdout(t *testing.T) {
	runner := systemHostRunner{}
	output, err := runner.Output("/bin/sh", []string{"-c", "printf '{\"name\":\"plateau-host\"}'; printf 'warning' >&2"})
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if string(output) != `{"name":"plateau-host"}` {
		t.Fatalf("output = %q, want JSON stdout only", output)
	}
}

func (runner *hostRunnerFake) Run(command string, args []string, stdin io.Reader, _ io.Writer, _ io.Writer) error {
	input := ""
	if stdin != nil {
		content, _ := io.ReadAll(stdin)
		input = string(content)
	}
	runner.calls = append(runner.calls, hostCommandCall{command: command, args: append([]string(nil), args...), stdin: input})
	runner.mutations = append(runner.mutations, hostCommandCall{command: command, args: append([]string(nil), args...), stdin: input})
	return nil
}

func (runner *hostRunnerFake) Output(command string, args []string) ([]byte, error) {
	runner.calls = append(runner.calls, hostCommandCall{command: command, args: append([]string(nil), args...)})
	key := strings.Join(args, "\x00")
	if len(runner.errorSequences[key]) > 0 {
		err := runner.errorSequences[key][0]
		runner.errorSequences[key] = runner.errorSequences[key][1:]
		if err != nil {
			return nil, err
		}
	}
	if err := runner.errors[key]; err != nil {
		return nil, err
	}
	if key == "list\x00--format=json" && len(runner.listOutputs) > 0 {
		output := runner.listOutputs[0]
		runner.listOutputs = runner.listOutputs[1:]
		return output, nil
	}
	if output, exists := runner.outputs[key]; exists {
		return output, nil
	}
	if key == strings.Join([]string{"shell", "plateau-host", "--", "sudo", "sh", "-eu", "-c", hostHealthCheck}, "\x00") {
		return nil, probeExit(1)
	}
	return runner.output, nil
}

func readyHostOutput(status string) []byte {
	return []byte(fmt.Sprintf(`{"name":"plateau-host","status":%q,"config":{"additionalDisks":[{"name":"plateau-data","format":false,"fsType":"btrfs"}]}}`, status))
}

func readyHostRunner(status string) *hostRunnerFake {
	return &hostRunnerFake{
		output: readyHostOutput(status),
		outputs: map[string][]byte{
			"disk\x00list\x00--json": []byte(`{"name":"plateau-data"}`),
			strings.Join([]string{"shell", "plateau-host", "--", "sudo", "incus", "storage", "list", "--format=json"}, "\x00"): []byte(`[{"name":"plateau","driver":"btrfs","status":"Created"}]`),
		},
	}
}

func TestHostEnsureCreatesOneDebianIncusHost(t *testing.T) {
	storageKey := strings.Join([]string{"shell", "plateau-host", "--", "sudo", "incus", "storage", "list", "--format=json"}, "\x00")
	runner := &hostRunnerFake{outputs: map[string][]byte{
		"disk\x00list\x00--json": nil,
		storageKey:               []byte(`[]`),
		strings.Join([]string{"shell", "plateau-host", "--", "readlink", "-f", "/dev/disk/by-label/lima-plateau-data"}, "\x00"): []byte("/dev/vdb1\n"),
	}}
	host := NewHost(context.Background(), &bytes.Buffer{}, &bytes.Buffer{})
	host.runner = runner

	if err := host.Ensure(); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	joined := ""
	for _, call := range runner.calls {
		joined += call.command + " " + strings.Join(call.args, " ") + "\n"
	}
	for _, required := range []string{
		"limactl disk create --tty=false plateau-data --size 1TiB --format raw",
		"limactl create --tty=false --name plateau-host --vm-type vz --arch aarch64 --cpus 4 --memory 8 --disk 32",
		"limactl stop plateau-host",
		`limactl edit --tty=false plateau-host --set .additionalDisks += [{"name":"plateau-data","format":true,"fsType":"btrfs"}]`,
		"sudo incus storage create plateau btrfs source=/dev/vdb1 source.wipe=true btrfs.mount_options=compress=zstd,discard=async,user_subvol_rm_allowed",
		`limactl edit --tty=false plateau-host --set (.additionalDisks[] | select(.name == "plateau-data") | .format) = false`,
		"mount -o remount,compress=zstd,discard=async /var/lib/incus/storage-pools/plateau",
		"btrfs property set /var/lib/incus/storage-pools/plateau compression zstd",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("commands:\n%s\nwant %q", joined, required)
		}
	}
	var createCall hostCommandCall
	for _, call := range runner.calls {
		if len(call.args) > 0 && call.args[0] == "create" {
			createCall = call
			break
		}
	}
	for _, required := range []string{"template:debian", "incus-base", "uidmap", "dnsmasq-base", "netcat-openbsd", "zstd", "name: incusbr0", "ipv4.address: 10.231.0.1/24", "ipv4.nat: \"true\"", "pool: default", "network: incusbr0", "guestPortRange", "30000", "39999"} {
		if !strings.Contains(createCall.stdin+incusPreseed, required) {
			t.Fatalf("host template does not contain %q", required)
		}
	}
	if !strings.Contains(createCall.stdin, "btrfs-progs") {
		t.Fatal("host template does not install Btrfs tooling")
	}
	if !strings.Contains(joined, "Plateau data disk is not empty; refusing to format it") || !strings.Contains(joined, "btrfs subvolume list /mnt/lima-plateau-data") {
		t.Fatalf("commands:\n%s\nmissing non-empty disk protection", joined)
	}
	if strings.Contains(createCall.stdin, "chromium") {
		t.Fatal("host template contains container workload package")
	}
	for _, forbidden := range []string{"disk delete", "storage delete", "incus move"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("host setup contains destructive migration command %q", forbidden)
		}
	}
}

func TestHostEnsureHealthyHostDoesNotRepeatSetup(t *testing.T) {
	runner := readyHostRunner("Running")
	// Only the combined, read-only probe may attest that the host is ready.
	runner.outputs[strings.Join([]string{"shell", "plateau-host", "--", "sudo", "sh", "-eu", "-c", hostHealthCheck}, "\x00")] = []byte("ready\n")
	host := NewHost(context.Background(), io.Discard, io.Discard)
	host.runner = runner
	if err := host.Ensure(); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("healthy host used %d processes, want disk, host, health, autostart checks", len(runner.calls))
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call.args, " "), "remount") || call.stdin != "" {
			t.Fatalf("healthy host repeated setup: %+v", call)
		}
	}
}

func TestHostEnsureRejectsUnavailableHealthProbeWithoutRepair(t *testing.T) {
	transportError := exec.Command("/bin/sh", "-c", "exit 255").Run()
	if transportError == nil {
		t.Fatal("expected fixture command to exit with status 255")
	}
	runner := readyHostRunner("Running")
	healthKey := strings.Join([]string{"shell", "plateau-host", "--", "sudo", "sh", "-eu", "-c", hostHealthCheck}, "\x00")
	runner.errors = map[string]error{healthKey: fmt.Errorf("SSH transport unavailable: %w", transportError)}
	host := NewHost(context.Background(), io.Discard, io.Discard)
	host.runner = runner
	host.wait = func(time.Duration) {}

	err := host.Ensure()
	if !errors.Is(err, transportError) {
		t.Errorf("ensure error = %v, want original transport error", err)
	}
	if len(runner.mutations) != 0 {
		t.Errorf("unavailable health probe triggered host mutations: %+v", runner.mutations)
	}
}

func TestHostAutostartInspectionFailureDoesNotTriggerRepair(t *testing.T) {
	runner := readyHostRunner("Running")
	runner.outputs[strings.Join([]string{"shell", "plateau-host", "--", "sudo", "sh", "-eu", "-c", hostHealthCheck}, "\x00")] = []byte("ready\n")
	failure := errors.New("launchctl permission denied")
	runner.errors = map[string]error{strings.Join([]string{"print", fmt.Sprintf("gui/%d/io.lima-vm.autostart.plateau-host", os.Getuid())}, "\x00"): failure}
	host := NewHost(context.Background(), io.Discard, io.Discard)
	host.runner = runner
	if err := host.Ensure(); !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
	if len(runner.mutations) != 0 {
		t.Fatalf("failed inspection triggered mutation: %+v", runner.mutations)
	}
}

func TestHostEnsureRejectsUnavailableSetupProbesWithoutRepair(t *testing.T) {
	transportError := exec.Command("/bin/sh", "-c", "exit 255").Run()
	missingError := exec.Command("/bin/sh", "-c", "exit 1").Run()
	if transportError == nil || missingError == nil {
		t.Fatal("expected fixture commands to return nonzero exit statuses")
	}
	for _, test := range []struct {
		name  string
		probe []string
	}{
		{
			name:  "dependencies",
			probe: []string{"test", "-x", "/usr/bin/incus", "-a", "-x", "/usr/bin/newuidmap", "-a", "-x", "/usr/bin/newgidmap", "-a", "-x", "/usr/sbin/dnsmasq", "-a", "-x", "/usr/sbin/mkfs.btrfs", "-a", "-x", "/usr/bin/nc", "-a", "-x", "/usr/bin/zstd"},
		},
		{name: "service", probe: []string{"sudo", "sh", "-eu", "-c", incusServiceCheck}},
		{name: "configuration", probe: []string{"sudo", "sh", "-eu", "-c", incusConfigurationCheck}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := readyHostRunner("Running")
			healthKey := strings.Join([]string{"shell", "plateau-host", "--", "sudo", "sh", "-eu", "-c", hostHealthCheck}, "\x00")
			probeKey := strings.Join(append([]string{"shell", "plateau-host", "--"}, test.probe...), "\x00")
			runner.errors = map[string]error{
				healthKey: missingError,
				probeKey:  fmt.Errorf("SSH transport unavailable: %w", transportError),
			}
			host := NewHost(context.Background(), io.Discard, io.Discard)
			host.runner = runner
			host.wait = func(time.Duration) {}

			err := host.Ensure()
			if !errors.Is(err, transportError) {
				t.Errorf("ensure error = %v, want original transport error", err)
			}
			if len(runner.mutations) != 0 {
				t.Errorf("unavailable %s probe triggered host mutations: %+v", test.name, runner.mutations)
			}
		})
	}
}

func TestHostEnsureRejectsMalformedHealthResponseWithoutRepair(t *testing.T) {
	for _, output := range []string{"", "unknown\n", "ready\nextra\n", `{"ready":true}`} {
		t.Run(fmt.Sprintf("%q", output), func(t *testing.T) {
			runner := readyHostRunner("Running")
			healthKey := strings.Join([]string{"shell", "plateau-host", "--", "sudo", "sh", "-eu", "-c", hostHealthCheck}, "\x00")
			runner.outputs[healthKey] = []byte(output)
			host := NewHost(context.Background(), io.Discard, io.Discard)
			host.runner = runner
			host.wait = func(time.Duration) {}

			if err := host.Ensure(); err == nil {
				t.Error("ensure accepted an unrecognized successful health response")
			}
			if len(runner.mutations) != 0 {
				t.Errorf("malformed health response triggered host mutations: %+v", runner.mutations)
			}
		})
	}
}

func TestHostEnsureRepairsMissingIncusInstallation(t *testing.T) {
	packageKey := strings.Join([]string{"shell", "plateau-host", "--", "test", "-x", "/usr/bin/incus", "-a", "-x", "/usr/bin/newuidmap", "-a", "-x", "/usr/bin/newgidmap", "-a", "-x", "/usr/sbin/dnsmasq", "-a", "-x", "/usr/sbin/mkfs.btrfs", "-a", "-x", "/usr/bin/nc", "-a", "-x", "/usr/bin/zstd"}, "\x00")
	configurationKey := strings.Join([]string{"shell", "plateau-host", "--", "sudo", "sh", "-eu", "-c", "incus storage show default >/dev/null\nincus network show incusbr0 >/dev/null\ntest \"$(incus profile device get default root pool)\" = default\ntest \"$(incus profile device get default eth0 network)\" = incusbr0"}, "\x00")
	runner := readyHostRunner("Running")
	runner.errors = map[string]error{packageKey: probeExit(1), configurationKey: probeExit(1)}
	runner.outputs[strings.Join([]string{"shell", "plateau-host", "--", "readlink", "-f", "/dev/disk/by-label/lima-plateau-data"}, "\x00")] = []byte("/dev/vdb1\n")
	for _, resource := range []string{"storage-pools", "networks", "profiles/default"} {
		output := []byte(`[]`)
		if resource == "profiles/default" {
			output = []byte(`{"devices":{},"config":{}}`)
		}
		probe := []string{"shell", "plateau-host", "--", "sudo", "incus", "query", "/1.0/" + resource + "?recursion=1"}
		if resource == "storage-pools" {
			probe = []string{"shell", "plateau-host", "--", "sudo", "incus", "storage", "list", "--format=json"}
		}
		runner.outputs[strings.Join(probe, "\x00")] = output
	}
	host := NewHost(context.Background(), &bytes.Buffer{}, &bytes.Buffer{})
	host.runner = runner

	if err := host.Ensure(); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	joined := ""
	for _, call := range runner.calls {
		joined += strings.Join(call.args, " ") + "\n"
	}
	for _, required := range []string{
		"apt-get update",
		"apt-get install -y --no-install-recommends incus-base uidmap dnsmasq-base btrfs-progs netcat-openbsd zstd",
		"systemctl enable --now incus.service incus.socket",
		"incus admin init --preseed",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("commands:\n%s\nwant %q", joined, required)
		}
	}
}

func TestHostNeverReinitializesConflictingOrUnreadableIncus(t *testing.T) {
	for _, inventory := range []string{`[{"name":"user-pool"}]`, `null`, `malformed`, ``} {
		t.Run(inventory, func(t *testing.T) {
			runner := readyHostRunner("Running")
			key := strings.Join([]string{"shell", "plateau-host", "--", "sudo", "sh", "-eu", "-c", incusConfigurationCheck}, "\x00")
			runner.errors = map[string]error{key: probeExit(1)}
			runner.outputs[strings.Join([]string{"shell", "plateau-host", "--", "sudo", "incus", "storage", "list", "--format=json"}, "\x00")] = []byte(inventory)
			host := NewHost(context.Background(), io.Discard, io.Discard)
			host.runner = runner
			if err := host.Ensure(); err == nil {
				t.Fatal("accepted conflicting/unreadable setup")
			}
			if len(runner.mutations) != 0 {
				t.Fatalf("mutated conflicting setup: %+v", runner.mutations)
			}
		})
	}
}

func TestHostEnsureInitializesIncusWithEmptyStorageQueryOutput(t *testing.T) {
	runner := readyHostRunner("Running")
	runner.outputs[strings.Join([]string{"shell", "plateau-host", "--", "readlink", "-f", "/dev/disk/by-label/lima-plateau-data"}, "\x00")] = []byte("/dev/vdb1\n")
	configurationKey := strings.Join([]string{"shell", "plateau-host", "--", "sudo", "sh", "-eu", "-c", incusConfigurationCheck}, "\x00")
	runner.errors = map[string]error{configurationKey: probeExit(1)}
	// Incus 6.0.4의 빈 저장소 query는 성공하면서 아무것도 출력하지 않습니다.
	runner.outputs[strings.Join([]string{"shell", "plateau-host", "--", "sudo", "incus", "query", "/1.0/storage-pools?recursion=1"}, "\x00")] = nil
	runner.outputs[strings.Join([]string{"shell", "plateau-host", "--", "sudo", "incus", "storage", "list", "--format=json"}, "\x00")] = []byte(`[]`)
	runner.outputs[strings.Join([]string{"shell", "plateau-host", "--", "sudo", "incus", "query", "/1.0/networks?recursion=1"}, "\x00")] = []byte(`[{"name":"lo","managed":false},{"name":"eth0","managed":false}]`)
	runner.outputs[strings.Join([]string{"shell", "plateau-host", "--", "sudo", "incus", "query", "/1.0/profiles/default?recursion=1"}, "\x00")] = []byte(`{"devices":{},"config":{}}`)
	host := NewHost(context.Background(), io.Discard, io.Discard)
	host.runner = runner
	host.wait = func(time.Duration) {}
	if err := host.Ensure(); err != nil {
		t.Fatalf("initialize fresh Incus: %v", err)
	}
	for _, call := range runner.mutations {
		if strings.Join(call.args, " ") == "shell plateau-host -- sudo incus admin init --preseed" {
			return
		}
	}
	t.Fatal("empty Incus installation was not initialized")
}

func TestHostEnsureStartsStoppedHostWithoutRecreatingIt(t *testing.T) {
	runner := readyHostRunner("Stopped")
	host := NewHost(context.Background(), &bytes.Buffer{}, &bytes.Buffer{})
	host.runner = runner

	if err := host.Ensure(); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	joined := ""
	for _, call := range runner.calls {
		joined += strings.Join(call.args, " ") + "\n"
	}
	if strings.Contains(joined, "create") || !strings.Contains(joined, "start --tty=false plateau-host") || strings.Contains(joined, "systemctl enable") || strings.Contains(joined, "autostart enable") {
		t.Fatalf("commands = %s", joined)
	}
}

func TestHostEnsureDisablesLimaFormattingAfterIncusOwnsDataDisk(t *testing.T) {
	runner := readyHostRunner("Running")
	runner.output = []byte(`{"name":"plateau-host","status":"Running","config":{"additionalDisks":[{"name":"plateau-data","format":true,"fsType":"btrfs"}]}}`)
	host := NewHost(context.Background(), &bytes.Buffer{}, &bytes.Buffer{})
	host.runner = runner

	if err := host.Ensure(); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	joined := ""
	for _, call := range runner.calls {
		joined += call.command + " " + strings.Join(call.args, " ") + "\n"
	}
	for _, required := range []string{
		"limactl stop plateau-host",
		`limactl edit --tty=false plateau-host --set (.additionalDisks[] | select(.name == "plateau-data") | .format) = false`,
		"limactl start --tty=false plateau-host",
		"sudo incus storage show plateau",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("commands:\n%s\nwant %q", joined, required)
		}
	}
}

func TestHostEnsureRejectsUnexpectedDataDiskFilesystem(t *testing.T) {
	runner := readyHostRunner("Running")
	runner.output = []byte(`{"name":"plateau-host","status":"Running","config":{"additionalDisks":[{"name":"plateau-data","format":false,"fsType":"ext4"}]}}`)
	host := NewHost(context.Background(), &bytes.Buffer{}, &bytes.Buffer{})
	host.runner = runner

	err := host.Ensure()
	if err == nil || !strings.Contains(err.Error(), `unsupported filesystem "ext4"`) {
		t.Fatalf("ensure error = %v", err)
	}
}

func TestHostEnsureRepairsMissingIncusServiceAndAutostartRegistration(t *testing.T) {
	serviceKey := strings.Join([]string{"shell", "plateau-host", "--", "sudo", "sh", "-eu", "-c", "systemctl is-enabled --quiet incus.service incus.socket\nsystemctl is-active --quiet incus.service incus.socket"}, "\x00")
	autostartKey := strings.Join([]string{"print", "gui/" + fmt.Sprint(os.Getuid()) + "/io.lima-vm.autostart.plateau-host"}, "\x00")
	runner := readyHostRunner("Running")
	runner.errors = map[string]error{serviceKey: probeExit(3), autostartKey: probeExit(113)}
	host := NewHost(context.Background(), &bytes.Buffer{}, &bytes.Buffer{})
	host.runner = runner

	if err := host.Ensure(); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	joined := ""
	for _, call := range runner.calls {
		joined += call.command + " " + strings.Join(call.args, " ") + "\n"
	}
	for _, required := range []string{"systemctl enable --now incus.service incus.socket", "limactl autostart enable --tty=false plateau-host"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("commands:\n%s\nwant %q", joined, required)
		}
	}
}

func TestHostEnsureWaitsForHostThatIsStillStarting(t *testing.T) {
	runner := &hostRunnerFake{listOutputs: [][]byte{
		readyHostOutput("Broken"),
		readyHostOutput("Running"),
	}}
	runner.outputs = readyHostRunner("Running").outputs
	host := NewHost(context.Background(), &bytes.Buffer{}, &bytes.Buffer{})
	host.runner = runner
	host.wait = func(time.Duration) {}

	if err := host.Ensure(); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	joined := ""
	hostListCount := 0
	for _, call := range runner.calls {
		joined += strings.Join(call.args, " ") + "\n"
		if reflect.DeepEqual(call.args, []string{"list", "--format=json"}) {
			hostListCount++
		}
	}
	if hostListCount != 2 || strings.Contains(joined, "create") || strings.Contains(joined, "start --tty=false") {
		t.Fatalf("commands = %s", joined)
	}
}

func TestHostEnsureWaitsForSSHBeforeCheckingDependencies(t *testing.T) {
	readinessKey := strings.Join([]string{"shell", "plateau-host", "--", "true"}, "\x00")
	runner := readyHostRunner("Running")
	runner.errorSequences = map[string][]error{readinessKey: {errors.New("SSH unavailable"), nil}}
	host := NewHost(context.Background(), &bytes.Buffer{}, &bytes.Buffer{})
	host.runner = runner
	host.wait = func(time.Duration) {}

	if err := host.Ensure(); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	joined := ""
	for _, call := range runner.calls {
		joined += strings.Join(call.args, " ") + "\n"
	}
	if strings.Count(joined, "shell plateau-host -- true") != 2 || strings.Contains(joined, "apt-get") {
		t.Fatalf("commands = %s", joined)
	}
}

func TestHostUpdateAllowsStablePackagesWithNewDependencies(t *testing.T) {
	runner := readyHostRunner("Running")
	host := NewHost(context.Background(), &bytes.Buffer{}, &bytes.Buffer{})
	host.runner = runner

	if err := host.Update(); err != nil {
		t.Fatalf("update: %v", err)
	}
	joined := ""
	for _, call := range runner.calls {
		joined += strings.Join(call.args, " ") + "\n"
	}
	if !strings.Contains(joined, "apt-get upgrade -y --with-new-pkgs") {
		t.Fatalf("commands:\n%s\nwant stable upgrade with new package dependencies", joined)
	}
}

func TestHostTransportsCommandsThroughFixedHost(t *testing.T) {
	runner := &hostRunnerFake{output: []byte(`output`)}
	host := NewHost(context.Background(), &bytes.Buffer{}, &bytes.Buffer{})
	host.runner = runner

	if err := host.Run([]string{"sudo", "incus", "start", "pg-a"}, strings.NewReader("input"), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	output, err := host.Output([]string{"sudo", "incus", "list", "--format=json"})
	if err != nil || string(output) != "output" {
		t.Fatalf("output = %q, error = %v", output, err)
	}
	want := [][]string{
		{"shell", "plateau-host", "--", "sudo", "incus", "start", "pg-a"},
		{"shell", "plateau-host", "--", "sudo", "incus", "list", "--format=json"},
	}
	for index, args := range want {
		if !reflect.DeepEqual(runner.calls[index].args, args) {
			t.Fatalf("call %d args = %v, want %v", index, runner.calls[index].args, args)
		}
	}
}
