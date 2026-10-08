package incus

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/creatrip/plateau/internal/adapter/hostlock"
	"github.com/creatrip/plateau/internal/domain"
	"github.com/klauspost/compress/zstd"
)

type transportCall struct {
	args  []string
	stdin string
}

type transportFake struct {
	outputSequences map[string][][]byte
	runErrors       map[string]error
	calls           []transportCall
	outputs         map[string][]byte
	copies          []string
	remoteFile      []byte
	runStdout       string
	runStderr       string
	runError        error
	exportError     error
}

func (transport *transportFake) Run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	input := ""
	if stdin != nil {
		content, _ := io.ReadAll(stdin)
		input = string(content)
	}
	transport.calls = append(transport.calls, transportCall{args: append([]string(nil), args...), stdin: input})
	if err := transport.runErrors[strings.Join(args, " ")]; err != nil {
		return err
	}
	if len(args) == 6 && args[0] == "sudo" && args[1] == "sh" && args[2] == "-c" && strings.Contains(args[3], "incus export") {
		if _, err := stdout.Write(transport.remoteFile); err != nil {
			return err
		}
		if _, err := io.WriteString(stderr, transport.runStderr); err != nil {
			return err
		}
		return transport.exportError
	}
	if stdout != nil {
		_, _ = io.WriteString(stdout, transport.runStdout)
	}
	if stderr != nil {
		_, _ = io.WriteString(stderr, transport.runStderr)
	}
	return transport.runError
}

func (transport *transportFake) Output(args []string) ([]byte, error) {
	transport.calls = append(transport.calls, transportCall{args: append([]string(nil), args...)})
	key := strings.Join(args, "\x00")
	if sequence := transport.outputSequences[key]; len(sequence) > 0 {
		transport.outputSequences[key] = sequence[1:]
		return sequence[0], nil
	}
	output, exists := transport.outputs[strings.Join(args, "\x00")]
	if !exists && len(args) == 5 && args[0] == "sudo" && args[1] == "incus" && args[2] == "list" {
		return []byte("[]"), nil
	}
	return output, nil
}

func (transport *transportFake) CopyToHost(localPath, hostPath string) error {
	transport.copies = append(transport.copies, "to "+localPath+" "+hostPath)
	content, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	transport.remoteFile = content
	return nil
}

func newManagerTest(t *testing.T) (Manager, *transportFake) {
	t.Helper()
	transport := &transportFake{outputs: map[string][]byte{}}
	manager := NewManager(context.Background(), transport, hostlock.Directory{Path: t.TempDir(), Context: context.Background()}, &bytes.Buffer{}, &bytes.Buffer{})
	manager.stdin = strings.NewReader("")
	manager.sshDirectory = t.TempDir()
	return manager, transport
}

func TestManagerClonesManagedDesktopImage(t *testing.T) {
	manager, transport := newManagerTest(t)
	aliasKey := strings.Join([]string{"sudo", "incus", "image", "alias", "list", "--format=json"}, "\x00")
	transport.outputs[aliasKey] = []byte(`[{"name":"plateau-desktop-v1","target":"abc123"}]`)
	wantPackages := "ca-certificates sudo openssh-server tigervnc-standalone-server openbox tint2 hsetroot xterm xfce4-terminal xfce4-settings thunar xdg-utils file fcitx5-hangul fcitx5-frontend-gtk3 dbus-x11 dbus-user-session libpam-systemd x11-xserver-utils novnc chromium fonts-noto-core fonts-noto-cjk fonts-noto-color-emoji"
	if containerPackages != wantPackages {
		t.Fatalf("container packages = %q, want %q", containerPackages, wantPackages)
	}

	if err := manager.Create("pg-a", 32001, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(transport.calls) != 5 {
		t.Fatalf("calls = %+v, want image lookup, init, VNC proxy, SSH proxy", transport.calls)
	}
	wantInit := []string{"sudo", "incus", "init", "plateau-desktop-v1", "pg-a", "--storage", "plateau", "--config", "boot.autostart=false", "--config", "user.plateau.managed=true", "--config", "user.plateau.name=pg-a", "--config", "user.plateau.vnc-port=32001", "--config", "user.plateau.desired=stopped", "--config", "user.plateau.pending=create", "--config", "user.plateau.group="}
	if !reflect.DeepEqual(transport.calls[2].args, wantInit) {
		t.Fatalf("init = %v, want %v", transport.calls[2].args, wantInit)
	}
	wantProxy := []string{"sudo", "incus", "config", "device", "add", "pg-a", "plateau-vnc", "proxy", "listen=tcp:127.0.0.1:32001", "connect=tcp:127.0.0.1:6080"}
	if !reflect.DeepEqual(transport.calls[3].args, wantProxy) {
		t.Fatalf("proxy = %v, want %v", transport.calls[3].args, wantProxy)
	}
	wantSSHProxy := []string{"sudo", "incus", "config", "device", "add", "pg-a", "plateau-ssh", "proxy", "listen=tcp:127.0.0.1:42001", "connect=tcp:127.0.0.1:22"}
	if !reflect.DeepEqual(transport.calls[4].args, wantSSHProxy) {
		t.Fatalf("SSH proxy = %v, want %v", transport.calls[4].args, wantSSHProxy)
	}
	joined := ""
	for _, call := range transport.calls {
		joined += strings.Join(call.args, " ") + "\n" + call.stdin
	}
	for _, forbidden := range []string{"images:debian/13 pg-a", "apt-get", "incus start pg-a", "incus stop pg-a", "plateau-pg-a", "hostnamectl", "config template create"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("create repeated image setup %q in:\n%s", forbidden, joined)
		}
	}
	if strings.Contains(strings.Join(transport.calls[2].args, " "), "--vm") {
		t.Fatal("create requested an Incus VM instead of a system container")
	}
}

func TestManagerCreateHidesIncusSuccessOutput(t *testing.T) {
	transport := &transportFake{
		outputs:   map[string][]byte{},
		runStdout: "Creating pg-a\nDevice plateau-vnc added to pg-a\n",
	}
	aliasKey := strings.Join([]string{"sudo", "incus", "image", "alias", "list", "--format=json"}, "\x00")
	transport.outputs[aliasKey] = []byte(`[{"name":"plateau-desktop-v1","target":"abc123"}]`)
	var stdout bytes.Buffer
	manager := NewManager(context.Background(), transport, hostlock.Directory{Path: t.TempDir(), Context: context.Background()}, &stdout, io.Discard)

	if err := manager.Create("pg-a", 32001, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no Incus implementation output", stdout.String())
	}
}

func TestManagerReportsInstanceNameInIncusErrors(t *testing.T) {
	transport := &transportFake{
		outputs:   map[string][]byte{},
		runStderr: "Error: Failed to start instance pg-a\n",
		runError:  errors.New("exit status 1"),
	}
	statusKey := strings.Join([]string{"sudo", "incus", "list", "^pg-a$", "--format=json"}, "\x00")
	transport.outputs[statusKey] = []byte(`[{"name":"pg-a","status":"Stopped","type":"container","config":{"user.plateau.managed":"true","user.plateau.name":"pg-a","user.plateau.vnc-port":"32001","boot.autostart":"true"}}]`)
	var stderr bytes.Buffer
	manager := NewManager(context.Background(), transport, hostlock.Directory{Path: t.TempDir(), Context: context.Background()}, io.Discard, &stderr)

	err := manager.Start("pg-a")
	if err == nil {
		t.Fatal("start succeeded, want Incus error")
	}
	if !strings.Contains(err.Error(), "pg-a") {
		t.Fatalf("error = %q, want container name", err)
	}
}

func TestManagerBuildsManagedDesktopImageOnce(t *testing.T) {
	manager, transport := newManagerTest(t)
	aliasKey := strings.Join([]string{"sudo", "incus", "image", "alias", "list", "--format=json"}, "\x00")
	builderKey := strings.Join([]string{"sudo", "incus", "list", "^plateau-image-builder-v1$", "--format=json"}, "\x00")
	transport.outputs[aliasKey] = []byte(`[]`)
	transport.outputs[builderKey] = []byte(`[]`)

	if err := manager.Create("pg-a", 32001, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	joined := ""
	provision := ""
	for _, call := range transport.calls {
		joined += strings.Join(call.args, " ") + "\n"
		if reflect.DeepEqual(call.args, []string{"sudo", "incus", "exec", "plateau-image-builder-v1", "--", "bash", "-s"}) {
			provision = call.stdin
		}
	}
	for _, required := range []string{
		"sudo incus init images:debian/13 plateau-image-builder-v1 --storage plateau",
		"sudo incus start plateau-image-builder-v1",
		"sudo incus stop plateau-image-builder-v1",
		"sudo incus publish plateau-image-builder-v1 --alias plateau-desktop-v1 --reuse",
		"sudo incus delete plateau-image-builder-v1",
		"sudo incus init plateau-desktop-v1 pg-a --storage plateau",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("commands:\n%s\nwant %q", joined, required)
		}
	}
	if provision == "" {
		t.Fatalf("missing managed image provisioning in:\n%s", joined)
	}
	for _, required := range []string{
		"openssh-server", "tigervnc-standalone-server", "openbox", "tint2", "hsetroot", "xterm", "novnc", "websockify", "chromium", "fonts-noto-core", "fonts-noto-cjk", "fonts-noto-color-emoji", "useradd", `export DISPLAY=:1`,
		"-AlwaysShared", "exec openbox-session", "xfce4-terminal", "fcitx5-hangul", "fcitx5-frontend-gtk3", "PAMName=login", "dbus-user-session", "libpam-systemd",
		"ListenAddress 127.0.0.1", "PermitRootLogin no", "PasswordAuthentication no", "KbdInteractiveAuthentication no", "AuthorizedKeysFile /etc/ssh/authorized_keys/%u", "AllowUsers plateau", "AllowTcpForwarding no",
		"install -d -o plateau -g plateau /home/plateau/.config /home/plateau/.config/openbox /home/plateau/.config/tint2 /home/plateau/.local /home/plateau/.local/share /home/plateau/.local/share/applications",
		"hsetroot -solid", "tint2 -c", "/home/plateau/.config/openbox/menu.xml", "plateau-terminal.desktop", "plateau-browser.desktop", `<item label="Terminal">`, `<item label="Chromium">`,
		"apt-get clean", "rm -rf /var/lib/apt/lists/*", "truncate -s 0 /etc/machine-id", "rm -f /var/lib/dbus/machine-id", "rm -f /etc/ssh/ssh_host_*", "rm -f /var/lib/systemd/random-seed",
	} {
		if !strings.Contains(provision, required) {
			t.Fatalf("provision script does not contain %q", required)
		}
	}
	for _, forbidden := range []string{"exec plateau-browser", "hermes-agent", "build-essential", "python3-dev", "libffi-dev", "git", "ripgrep", "ffmpeg", "curl", "xz-utils", "at-spi2-core", "dbus-launch", "force-renderer-accessibility", "fonts-dejavu-core", "xsetroot"} {
		if strings.Contains(provision, forbidden) {
			t.Fatalf("provision script contains unrelated dependency %q", forbidden)
		}
	}
	if strings.Count(joined, "apt-get") != 0 {
		t.Fatal("commands must pipe package installation through builder stdin only")
	}
}

func TestManagerUpdateRebuildsManagedDesktopImage(t *testing.T) {
	manager, transport := newManagerTest(t)
	aliasKey := strings.Join([]string{"sudo", "incus", "image", "alias", "list", "--format=json"}, "\x00")
	builderKey := strings.Join([]string{"sudo", "incus", "list", "^plateau-image-builder-v1$", "--format=json"}, "\x00")
	transport.outputs[aliasKey] = []byte(`[{"name":"plateau-desktop-v1","target":"old-fingerprint"}]`)
	transport.outputs[builderKey] = []byte(`[]`)

	if err := manager.UpdateImage(); err != nil {
		t.Fatalf("update image: %v", err)
	}
	joined := ""
	update := ""
	for _, call := range transport.calls {
		joined += strings.Join(call.args, " ") + "\n"
		if reflect.DeepEqual(call.args, []string{"sudo", "incus", "exec", "plateau-image-builder-v1", "--", "bash", "-s"}) {
			update = call.stdin
		}
	}
	if !strings.Contains(joined, "sudo incus init plateau-desktop-v1 plateau-image-builder-v1 --storage plateau") {
		t.Fatalf("commands:\n%s\nmissing CoW clone of managed image", joined)
	}
	if strings.Contains(joined, "sudo incus init images:debian/13 plateau-image-builder-v1") {
		t.Fatalf("commands:\n%s\nupdate fetched the remote base image", joined)
	}
	if !strings.Contains(joined, "sudo incus publish plateau-image-builder-v1 --alias plateau-desktop-v1 --reuse") {
		t.Fatalf("commands:\n%s\nmissing image replacement", joined)
	}
	if strings.Contains(joined, "sudo incus image delete old-fingerprint") {
		t.Fatalf("commands:\n%s\nremoved an image already managed by publish --reuse", joined)
	}
	for _, required := range []string{"apt-get upgrade -y --with-new-pkgs", "truncate -s 0 /etc/machine-id", "rm -f /etc/ssh/ssh_host_*", "rm -f /var/lib/systemd/random-seed"} {
		if !strings.Contains(update, required) {
			t.Fatalf("managed image update script does not contain %q", required)
		}
	}
}

func TestManagerMapsIncusContainerStatus(t *testing.T) {
	manager, transport := newManagerTest(t)
	key := strings.Join([]string{"sudo", "incus", "list", "^pg-a$", "--format=json"}, "\x00")
	for incusStatus, want := range map[string]domain.RuntimeStatus{"Running": domain.StatusRunning, "Stopped": domain.StatusStopped, "Error": domain.StatusBroken} {
		transport.outputs[key] = []byte(fmt.Sprintf(`[{"name":"pg-a","status":%q,"type":"container","config":{"user.plateau.managed":"true","user.plateau.name":"pg-a","user.plateau.vnc-port":"32001","boot.autostart":"true"}}]`, incusStatus))

		status, err := manager.Status("pg-a")
		if err != nil {
			t.Fatalf("status %s: %v", incusStatus, err)
		}
		if status != want {
			t.Fatalf("status = %q, want %q", status, want)
		}
	}
}

func TestManagerReadsExclusiveBtrfsUsageForContainersInOneCall(t *testing.T) {
	manager, transport := newManagerTest(t)
	key := strings.Join([]string{
		"sudo", "btrfs", "filesystem", "du", "--raw", "--summarize", "--",
		"/var/lib/incus/storage-pools/plateau/containers/pg-a",
		"/var/lib/incus/storage-pools/plateau/containers/worker",
	}, "\x00")
	transport.outputs[key] = []byte("     Total   Exclusive  Set shared  Filename\n1560227840    16785408   951611392  /var/lib/incus/storage-pools/plateau/containers/pg-a\n2147483648   536870912  1073741824  /var/lib/incus/storage-pools/plateau/containers/worker\n")

	usage, err := manager.DiskUsage([]domain.Name{"pg-a", "worker"})
	if err != nil {
		t.Fatalf("disk usage: %v", err)
	}
	want := map[domain.Name]uint64{"pg-a": 16785408, "worker": 536870912}
	if !reflect.DeepEqual(usage, want) {
		t.Fatalf("usage = %v, want %v", usage, want)
	}
	if len(transport.calls) != 1 {
		t.Fatalf("calls = %d, want one batched Btrfs query", len(transport.calls))
	}
}

func TestManagerRejectsIncompleteBtrfsUsageOutput(t *testing.T) {
	manager, transport := newManagerTest(t)
	key := strings.Join([]string{"sudo", "btrfs", "filesystem", "du", "--raw", "--summarize", "--", "/var/lib/incus/storage-pools/plateau/containers/pg-a"}, "\x00")
	transport.outputs[key] = []byte("Total Exclusive Set shared Filename\n")

	if _, err := manager.DiskUsage([]domain.Name{"pg-a"}); err == nil {
		t.Fatal("incomplete disk usage output succeeded")
	}
}

func TestManagerRejectsUnmanagedIncusContainerWithSameName(t *testing.T) {
	manager, transport := newManagerTest(t)
	key := strings.Join([]string{"sudo", "incus", "list", "^pg-a$", "--format=json"}, "\x00")
	transport.outputs[key] = []byte(`[{"name":"pg-a","status":"Running","type":"container","config":{}}]`)

	if _, err := manager.Status("pg-a"); err == nil || !strings.Contains(err.Error(), "not managed by Plateau") {
		t.Fatalf("status error = %v, want unmanaged container rejection", err)
	}
}

func TestManagerUsesExactInstanceNamesForLifecycle(t *testing.T) {
	manager, transport := newManagerTest(t)
	statusKey := strings.Join([]string{"sudo", "incus", "list", "^pg-a$", "--format=json"}, "\x00")
	transport.outputs[statusKey] = []byte(`[{"name":"pg-a","status":"Stopped","type":"container","config":{"user.plateau.managed":"true","user.plateau.name":"pg-a","user.plateau.vnc-port":"32001","boot.autostart":"true"}}]`)

	if err := manager.Start("pg-a"); err != nil {
		t.Fatal(err)
	}
	transport.outputs[statusKey] = []byte(`[{"name":"pg-a","status":"Running","type":"container","config":{"user.plateau.managed":"true","user.plateau.name":"pg-a","user.plateau.vnc-port":"32001","boot.autostart":"true"}}]`)
	if err := manager.EnableAutostart("pg-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.DisableAutostart("pg-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop("pg-a"); err != nil {
		t.Fatal(err)
	}
	transport.outputs[statusKey] = []byte(`[{"name":"pg-a","status":"Stopped","type":"container","config":{"user.plateau.managed":"true","user.plateau.name":"pg-a","user.plateau.vnc-port":"32001","boot.autostart":"true"}}]`)
	if err := manager.Delete("pg-a"); err != nil {
		t.Fatal(err)
	}

	joined := ""
	for _, call := range transport.calls {
		joined += strings.Join(call.args, " ") + "\n" + call.stdin
	}
	for _, required := range []string{
		"sudo incus start pg-a",
		"sudo incus config set pg-a boot.autostart=true user.plateau.desired=running",
		"sudo incus config set pg-a boot.autostart=false user.plateau.desired=stopped",
		"sudo incus stop pg-a",
		"sudo incus delete pg-a",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("commands:\n%s\nwant %q", joined, required)
		}
	}
	for _, forbidden := range []string{"plateau-pg-a", "incus exec", "hostnamectl", "/etc/hosts", "config template create", "name-ready"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("lifecycle commands unexpectedly rewrite instance identity:\n%s\nfound %q", joined, forbidden)
		}
	}
}

func TestManagerDeleteRemovesRememberedContainerHostKey(t *testing.T) {
	manager, _ := newManagerTest(t)
	knownHostsPath := filepath.Join(manager.sshDirectory, "known_hosts-pg-a")
	if err := os.WriteFile(knownHostsPath, []byte("old host key"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := manager.Delete("pg-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(knownHostsPath); !os.IsNotExist(err) {
		t.Fatalf("known hosts stat error = %v, want removed file", err)
	}
}

func TestFailedDeleteKeepsRememberedHostKey(t *testing.T) {
	manager, transport := newManagerTest(t)
	path := filepath.Join(manager.sshDirectory, "known_hosts-pg-a")
	if err := os.WriteFile(path, []byte("trusted key"), 0o600); err != nil {
		t.Fatal(err)
	}
	transport.outputs["sudo\x00incus\x00list\x00^pg-a$\x00--format=json"] = []byte(`[{"name":"pg-a","status":"Stopped","type":"container","config":{"user.plateau.managed":"true","user.plateau.name":"pg-a","user.plateau.vnc-port":"32001","boot.autostart":"false"}}]`)
	transport.runError = errors.New("container is busy")
	if err := manager.Delete("pg-a"); err == nil {
		t.Fatal("delete unexpectedly succeeded")
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "trusted key" {
		t.Fatalf("failed deletion discarded SSH trust: %q, %v", content, err)
	}
}

func TestManagerUsesContainerSSHThroughLimaStreamProxy(t *testing.T) {
	manager, transport := newManagerTest(t)
	manager.sshDirectory = filepath.Join(t.TempDir(), "Application Support", "plateau", "ssh")
	queryKey := strings.Join([]string{"sudo", "incus", "query", "/1.0/instances/pg-a"}, "\x00")
	transport.outputs[queryKey] = []byte(`{"config":{"user.plateau.managed":"true","user.plateau.name":"pg-a"},"devices":{}}`)
	managedDirectory := filepath.Join(t.TempDir(), "managed tools", "lima", "bin")
	if err := os.MkdirAll(managedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	limactlPath := filepath.Join(managedDirectory, "limactl")
	if err := os.WriteFile(limactlPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", managedDirectory)
	sshLogPath := filepath.Join(t.TempDir(), "ssh-arguments")
	t.Setenv("PLATEAU_SSH_TEST_LOG", sshLogPath)
	manager.sshPath = filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(manager.sshPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$PLATEAU_SSH_TEST_LOG\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := manager.PrepareSSH("pg-a", 32001); err != nil {
		t.Fatal(err)
	}
	if err := manager.SSH("pg-a", 32001); err != nil {
		t.Fatalf("SSH: %v", err)
	}
	joined := ""
	for _, call := range transport.calls {
		joined += strings.Join(call.args, " ") + "\n" + call.stdin
	}
	for _, required := range []string{
		"sudo incus query /1.0/instances/pg-a",
		"sudo incus config device add pg-a plateau-ssh proxy listen=tcp:127.0.0.1:42001 connect=tcp:127.0.0.1:22",
		"sudo incus exec pg-a -- bash -s -- ssh-ed25519 ",
		"apt-get install -y --no-install-recommends openssh-server",
		"PasswordAuthentication no",
		"systemctl reload-or-restart ssh.service",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("calls:\n%s\nwant %q", joined, required)
		}
	}
	for _, call := range transport.calls {
		if len(call.args) >= 7 && reflect.DeepEqual(call.args[:7], []string{"sudo", "incus", "exec", "pg-a", "--", "bash", "-s"}) {
			if len(call.args) != 9 || call.args[7] != "--" || !strings.HasPrefix(call.args[8], "ssh-ed25519 ") {
				t.Fatalf("SSH preparation arguments = %v, want only the public key after --", call.args)
			}
		}
	}
	for _, forbidden := range []string{"plateau-pg-a", "hostnamectl", "/etc/hosts", "public_name", "runtime_name"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("SSH setup unexpectedly rewrites instance identity:\n%s\nfound %q", joined, forbidden)
		}
	}
	if _, err := os.Stat(filepath.Join(manager.sshDirectory, "id_ed25519")); err != nil {
		t.Fatalf("generated private key: %v", err)
	}
	sshArgumentBytes, err := os.ReadFile(sshLogPath)
	if err != nil {
		t.Fatalf("read SSH arguments: %v", err)
	}
	sshArguments := strings.ReplaceAll(string(sshArgumentBytes), "\n", " ")
	for _, required := range []string{
		"-F /dev/null", "IdentitiesOnly=yes", "IdentityAgent=none", "StrictHostKeyChecking=accept-new", "LogLevel=ERROR",
		fmt.Sprintf("UserKnownHostsFile=%q", filepath.Join(manager.sshDirectory, "known_hosts-pg-a")),
		"ProxyCommand=exec '" + limactlPath + "' shell --tty=false plateau-host -- /usr/bin/nc 127.0.0.1 42001",
		"plateau@pg-a",
	} {
		if !strings.Contains(sshArguments, required) {
			t.Fatalf("SSH arguments = %q, want %q", sshArguments, required)
		}
	}
}

func TestManagerSkipsContainerSSHRepairWhenConfigurationIsCurrent(t *testing.T) {
	manager, transport := newManagerTest(t)
	publicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFake plateau"
	readyDigest := sha256.Sum256([]byte(containerSSHReadyVersion + "\n" + publicKey))
	queryKey := strings.Join([]string{"sudo", "incus", "query", "/1.0/instances/pg-a"}, "\x00")
	transport.outputs[queryKey] = []byte(fmt.Sprintf(`{"config":{"user.plateau.managed":"true","user.plateau.name":"pg-a","user.plateau.ssh-ready":"%s"},"devices":{"plateau-ssh":{"type":"proxy","listen":"tcp:127.0.0.1:42001","connect":"tcp:127.0.0.1:22"}}}`, fmt.Sprintf("%x", readyDigest)))
	if err := os.WriteFile(filepath.Join(manager.sshDirectory, "id_ed25519"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager.sshDirectory, "id_ed25519.pub"), []byte(publicKey+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	managedDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(managedDirectory, "limactl"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", managedDirectory)
	manager.sshPath = filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(manager.sshPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := manager.PrepareSSH("pg-a", 32001); err != nil {
		t.Fatal(err)
	}
	if err := manager.SSH("pg-a", 32001); err != nil {
		t.Fatalf("SSH: %v", err)
	}
	for _, call := range transport.calls {
		if strings.Contains(strings.Join(call.args, " "), "incus exec") {
			t.Fatalf("current SSH configuration was repaired again: %+v", transport.calls)
		}
	}
}

func TestManagerRefusesUnexpectedContainerSSHProxy(t *testing.T) {
	manager, transport := newManagerTest(t)
	queryKey := strings.Join([]string{"sudo", "incus", "query", "/1.0/instances/pg-a"}, "\x00")
	transport.outputs[queryKey] = []byte(`{"config":{"user.plateau.managed":"true","user.plateau.name":"pg-a"},"devices":{"plateau-ssh":{"type":"proxy","listen":"tcp:0.0.0.0:42001","connect":"tcp:127.0.0.1:22"}}}`)

	err := manager.PrepareSSH("pg-a", 32001)
	if err == nil || !strings.Contains(err.Error(), "unexpected configuration") {
		t.Fatalf("SSH error = %v, want unexpected proxy configuration", err)
	}
	if len(transport.calls) != 1 {
		t.Fatalf("calls = %+v, want inspection only", transport.calls)
	}
}

func TestManagerUpdatesContainerFromDebianStable(t *testing.T) {
	manager, transport := newManagerTest(t)

	if err := manager.UpdateGuest("pg-a"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(transport.calls) != 1 {
		t.Fatalf("calls = %+v, want one update", transport.calls)
	}
	want := []string{"sudo", "incus", "exec", "pg-a", "--", "bash", "-s"}
	if !reflect.DeepEqual(transport.calls[0].args, want) {
		t.Fatalf("args = %v, want %v", transport.calls[0].args, want)
	}
	for _, required := range []string{
		"apt-get upgrade -y --with-new-pkgs", "tint2", "hsetroot", "xterm", "fonts-noto-core", "fonts-noto-cjk", "fonts-noto-color-emoji", `export DISPLAY=:1`,
		"-AlwaysShared", "exec openbox-session", "xfce4-terminal", "fcitx5-hangul", "fcitx5-frontend-gtk3", "PAMName=login", "dbus-user-session", "libpam-systemd",
		"install -d -o plateau -g plateau /home/plateau/.config /home/plateau/.config/openbox /home/plateau/.config/tint2 /home/plateau/.local /home/plateau/.local/share /home/plateau/.local/share/applications",
		"hsetroot -solid", "tint2 -c", "/home/plateau/.config/openbox/menu.xml", "plateau-terminal.desktop", "plateau-browser.desktop", `<item label="Terminal">`, `<item label="Chromium">`,
		"systemctl restart plateau-vnc.service plateau-novnc.service", "apt-get clean", "rm -rf /var/lib/apt/lists/*",
	} {
		if !strings.Contains(transport.calls[0].stdin, required) {
			t.Fatalf("update script does not contain %q", required)
		}
	}
	for _, forbidden := range []string{"exec plateau-browser", "hermes-agent", "build-essential", "python3-dev", "libffi-dev", "git", "ripgrep", "ffmpeg", "curl", "xz-utils", "at-spi2-core", "dbus-launch", "force-renderer-accessibility", "fonts-dejavu-core", "xsetroot"} {
		if strings.Contains(transport.calls[0].stdin, forbidden) {
			t.Fatalf("update script contains unrelated dependency %q", forbidden)
		}
	}
}

func TestManagerOpensNoVNCOnMacLoopback(t *testing.T) {
	manager, _ := newManagerTest(t)
	opened := ""
	waited := ""
	manager.waitForVNC = func(address string) error {
		waited = address
		return nil
	}
	manager.openURL = func(url string) error {
		opened = url
		return nil
	}

	if err := manager.OpenVNC(32001); err != nil {
		t.Fatalf("open VNC: %v", err)
	}
	if waited != "127.0.0.1:32001" || opened != "http://127.0.0.1:32001/vnc.html?autoconnect=true&resize=scale&shared=1" {
		t.Fatalf("waited = %q, opened = %q", waited, opened)
	}
}

func incusExportPayload(t *testing.T, instance domain.Instance, legacy ...bool) []byte {
	t.Helper()
	var payload bytes.Buffer
	var compressed io.WriteCloser
	if len(legacy) > 0 && legacy[0] {
		compressed = gzip.NewWriter(&payload)
	} else {
		var err error
		compressed, err = zstd.NewWriter(&payload, zstd.WithEncoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
	}
	archive := tar.NewWriter(compressed)
	config := fmt.Sprintf("container:\n  name: %s\n  config: &instance_config\n    boot.autostart: \"true\"\n    user.plateau.managed: \"true\"\n    user.plateau.name: %s\n    user.plateau.desired: %s\n    user.plateau.vnc-port: \"%d\"\n  expanded_config: *instance_config\n  description: retained metadata\n", instance.Name, instance.Name, instance.DesiredState, instance.VNCPort)
	if instance.Group != "" {
		config = strings.Replace(config, "  expanded_config:", fmt.Sprintf("    user.plateau.group: %q\n  expanded_config:", instance.Group), 1)
	}
	index := fmt.Sprintf("name: %s\ntype: container\nconfig:\n", instance.Name) + strings.TrimSuffix("  "+strings.ReplaceAll(config, "\n", "\n  "), "  ")
	for _, entry := range []struct{ name, content string }{
		{"backup/index.yaml", index}, {"backup/container/backup.yaml", config}, {"backup/container/rootfs/probe", "untouched workload bytes"},
	} {
		content := []byte(entry.content)
		header := &tar.Header{Name: entry.name, Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(entry.name, "/rootfs/probe") {
			header.Mode, header.Uid, header.Gid = 0640, 1000, 1000
			header.ModTime = time.Unix(1700000000, 0)
			header.Xattrs = map[string]string{"user.plateau-probe": "retained attribute"}
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	for _, header := range []*tar.Header{
		{Name: "backup/container/rootfs/probe-symlink", Typeflag: tar.TypeSymlink, Linkname: "probe", Mode: 0777, Uid: 1000, Gid: 1000},
		{Name: "backup/container/rootfs/probe-hardlink", Typeflag: tar.TypeLink, Linkname: "backup/container/rootfs/probe", Mode: 0640, Uid: 1000, Gid: 1000},
	} {
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return payload.Bytes()
}

func TestManagerBacksUpInspectsAndRestoresIncusExport(t *testing.T) {
	manager, transport := newManagerTest(t)
	instance := domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}
	transport.remoteFile = incusExportPayload(t, instance)
	bundlePath := filepath.Join(t.TempDir(), "pg-a.plateau")

	if err := manager.Backup(instance, bundlePath); err != nil {
		t.Fatalf("backup: %v", err)
	}
	inspected, err := manager.ReadBackupManifest(bundlePath)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if inspected != instance {
		t.Fatalf("manifest = %+v, want %+v", inspected, instance)
	}
	manifest, err := readPlateauBundle(bundlePath, "")
	if err != nil {
		t.Fatal(err)
	}
	staging := "plateau-import-" + manifest.sourceID[:24]
	listKey := strings.Join([]string{"sudo", "incus", "list", "^" + staging + "$", "--format=json"}, "\x00")
	staged := []byte(fmt.Sprintf(`[{"name":%q,"status":"Stopped","type":"container","config":{"user.plateau.managed":"true","user.plateau.name":"pg-a","user.plateau.vnc-port":"32001","user.plateau.desired":"running","boot.autostart":"false","user.plateau.pending":"restore","user.plateau.restore-id":%q},"devices":{"plateau-vnc":{"type":"proxy","connect":"tcp:127.0.0.1:6080"}}}]`, staging, manifest.sourceID))
	transport.outputSequences = map[string][][]byte{listKey: {[]byte("[]"), staged}}
	if err := manager.RestoreBackup(bundlePath, instance); err != nil {
		t.Fatalf("restore: %v", err)
	}
	joined := ""
	for _, call := range transport.calls {
		joined += strings.Join(call.args, " ") + "\n"
	}
	for _, required := range []string{
		`incus export "$1" /dev/fd/3`, `--instance-only --compression="zstd -1"`, "sudo incus import", "pg-a",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("commands:\n%s\nwant %q", joined, required)
		}
	}
	if strings.Contains(joined, "plateau-pg-a") || strings.Contains(joined, "config template create") {
		t.Fatalf("backup or restore rewrote instance identity:\n%s", joined)
	}
	if len(transport.copies) != 0 {
		t.Fatalf("copies = %v", transport.copies)
	}
}

func TestBackupReportsExportProgressWithoutPollutingStdout(t *testing.T) {
	manager, transport := newManagerTest(t)
	instance := domain.Instance{Name: "progress-test", VNCPort: 32001, DesiredState: domain.DesiredStopped}
	transport.remoteFile = incusExportPayload(t, instance)
	transport.runStderr = "Backing up instance: 64MB (32MB/s)\n"
	if err := manager.Backup(instance, filepath.Join(t.TempDir(), "test.plateau")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(manager.stderr.(*bytes.Buffer).String(), transport.runStderr) {
		t.Fatal("export progress was hidden")
	}
	if manager.stdout.(*bytes.Buffer).Len() != 0 {
		t.Fatal("progress polluted the backup path output")
	}
}

func TestBackupDiscardsPartialExportWithoutPublishingOrOverwriting(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			manager, transport := newManagerTest(t)
			instance := domain.Instance{Name: "partial-test", VNCPort: 32001, DesiredState: domain.DesiredStopped}
			transport.remoteFile = incusExportPayload(t, instance)
			failure := errors.New("export interrupted after partial output")
			transport.exportError = failure
			directory := t.TempDir()
			destination := filepath.Join(directory, "test.plateau")
			if existing {
				if err := os.WriteFile(destination, []byte("original backup"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := manager.Backup(instance, destination); !errors.Is(err, failure) {
				t.Fatalf("backup error = %v, want export failure", err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			if existing {
				data, err := os.ReadFile(destination)
				if err != nil || string(data) != "original backup" || len(entries) != 1 {
					t.Fatalf("original changed or temporary files remained: %q, %v, %v", data, entries, err)
				}
			} else if len(entries) != 0 {
				t.Fatalf("partial backup was published or retained: %v", entries)
			}
		})
	}
}

func TestManagerRejectsTamperedPlateauBundle(t *testing.T) {
	manager, transport := newManagerTest(t)
	instance := domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredStopped}
	transport.remoteFile = incusExportPayload(t, instance)
	bundlePath := filepath.Join(t.TempDir(), "pg-a.plateau")
	if err := manager.Backup(instance, bundlePath); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(bundlePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	offset, err := file.Seek(-1, io.SeekEnd)
	if err != nil {
		t.Fatal(err)
	}
	lastByte := []byte{0}
	if _, err := file.ReadAt(lastByte, offset); err != nil {
		t.Fatal(err)
	}
	lastByte[0] ^= 0xff
	if _, err := file.WriteAt(lastByte, offset); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readPlateauBundle(bundlePath, ""); err == nil {
		t.Fatal("tampered bundle passed inspection")
	}
}

func TestRestoreRebindsOnlyHostLocalPorts(t *testing.T) {
	manager, transport := newManagerTest(t)
	original := domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}
	transport.remoteFile = incusExportPayload(t, original)
	path := filepath.Join(t.TempDir(), "backup.plateau")
	if err := manager.Backup(original, path); err != nil {
		t.Fatal(err)
	}
	manifest, err := readPlateauBundle(path, "")
	if err != nil {
		t.Fatal(err)
	}
	staging := "plateau-import-" + manifest.sourceID[:24]
	key := strings.Join([]string{"sudo", "incus", "list", "^" + staging + "$", "--format=json"}, "\x00")
	staged := []byte(fmt.Sprintf(`[{"name":%q,"status":"Stopped","type":"container","config":{"user.plateau.managed":"true","user.plateau.name":"pg-a","user.plateau.vnc-port":"32001","user.plateau.desired":"running","boot.autostart":"false","user.plateau.pending":"restore","user.plateau.restore-id":%q},"devices":{"plateau-vnc":{"type":"proxy","connect":"tcp:127.0.0.1:6080"},"plateau-ssh":{"type":"proxy","connect":"tcp:127.0.0.1:22"}}}]`, staging, manifest.sourceID))
	transport.outputSequences = map[string][][]byte{key: {[]byte("[]"), staged}}
	destination := original
	destination.VNCPort = 30003
	if err := manager.RestoreBackup(path, destination); err != nil {
		t.Fatal(err)
	}
	var commands []string
	for _, call := range transport.calls {
		commands = append(commands, strings.Join(call.args, " "))
	}
	for _, expected := range []string{
		"sudo incus config device set " + staging + " plateau-vnc listen=tcp:127.0.0.1:30003",
		"sudo incus config device set " + staging + " plateau-ssh listen=tcp:127.0.0.1:40003",
		"sudo incus config set " + staging + " user.plateau.vnc-port=30003",
		"sudo incus move " + staging + " pg-a",
	} {
		if !strings.Contains(strings.Join(commands, "\n"), expected) {
			t.Errorf("missing %q in %v", expected, commands)
		}
	}
}

func TestCompleteOnlyClearsThePendingCheckpoint(t *testing.T) {
	manager, transport := newManagerTest(t)
	if err := manager.Complete("a"); err != nil {
		t.Fatal(err)
	}
	if len(transport.calls) != 1 || strings.Join(transport.calls[0].args, " ") != "sudo incus config unset a user.plateau.pending" {
		t.Fatalf("unexpected completion: %+v", transport.calls)
	}
	if err := manager.Complete("../invalid"); err == nil {
		t.Fatal("invalid name accepted")
	}
	failure := errors.New("connection lost")
	transport.runError = failure
	if err := manager.Complete("a"); !errors.Is(err, failure) {
		t.Fatalf("completion error lost: %v", err)
	}
}
