package incus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/creatrip/plateau/internal/adapter/process"
	"github.com/creatrip/plateau/internal/application"
	"github.com/creatrip/plateau/internal/domain"
)

type Transport interface {
	Run(arguments []string, stdin io.Reader, stdout, stderr io.Writer) error
	Output(arguments []string) ([]byte, error)
}

type Manager struct {
	runner        process.Runner
	locks         application.Locker
	transport     Transport
	stdin         io.Reader
	stdout        io.Writer
	stderr        io.Writer
	openURL       func(string) error
	waitForVNC    func(string) error
	sshDirectory  string
	sshPath       string
	sshKeygenPath string
}

const (
	managedStoragePool       = "plateau"
	managedImageAlias        = "plateau-desktop-v1"
	imageBuilderName         = "plateau-image-builder-v1"
	containerSSHReadyVersion = "plateau-ssh-v4"
)

func NewManager(ctx context.Context, transport Transport, locks application.Locker, stdout, stderr io.Writer) Manager {
	sshDirectory := ""
	if configDirectory, err := os.UserConfigDir(); err == nil {
		sshDirectory = filepath.Join(configDirectory, "plateau", "ssh")
	}
	manager := Manager{
		runner:        process.Runner{Context: ctx},
		locks:         locks,
		transport:     transport,
		stdin:         os.Stdin,
		stdout:        stdout,
		stderr:        stderr,
		sshDirectory:  sshDirectory,
		sshPath:       "/usr/bin/ssh",
		sshKeygenPath: "/usr/bin/ssh-keygen",
	}
	manager.openURL = func(url string) error {
		if err := manager.runner.Run("/usr/bin/open", []string{url}, nil, manager.stdout, manager.stderr); err != nil {
			return fmt.Errorf("open VNC page: %w", err)
		}
		return nil
	}
	manager.waitForVNC = func(address string) error {
		deadline := time.Now().Add(30 * time.Second)
		for {
			connection, err := net.DialTimeout("tcp", address, 500*time.Millisecond)
			if err == nil {
				_ = connection.Close()
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("wait for VNC at %s: %w", address, err)
			}
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return manager
}

func (manager Manager) runCommand(arguments []string, stdin io.Reader, stdout io.Writer, hiddenName, displayName string) error {
	var commandErrors bytes.Buffer
	runErr := manager.transport.Run(arguments, stdin, stdout, &commandErrors)
	message := commandErrors.String()
	if hiddenName != "" {
		message = strings.ReplaceAll(message, hiddenName, displayName)
	}
	if runErr != nil {
		if message = strings.TrimSpace(message); message != "" {
			return fmt.Errorf("%w: %s", runErr, message)
		}
		return runErr
	}
	if message != "" {
		if _, err := io.WriteString(manager.stderr, message); err != nil {
			return fmt.Errorf("write command warning: %w", err)
		}
	}
	return nil
}

func (manager Manager) AllocateVNCPort() (uint16, error) {
	output, err := manager.transport.Output([]string{"sudo", "incus", "list", "--format=json"})
	if err != nil {
		return 0, fmt.Errorf("list Incus ports: %w", err)
	}
	var records []struct {
		Config  map[string]string            `json:"config"`
		Devices map[string]map[string]string `json:"expanded_devices"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(&records); err != nil {
		return 0, fmt.Errorf("decode Incus ports: %w", err)
	}
	if records == nil || decoder.Decode(&struct{}{}) != io.EOF {
		return 0, fmt.Errorf("decode Incus ports: trailing data")
	}
	used := map[uint16]bool{}
	for _, record := range records {
		port, err := strconv.ParseUint(record.Config["user.plateau.vnc-port"], 10, 16)
		if err == nil && port >= 30000 && port <= 39999 {
			used[uint16(port)] = true
		}
		// User-managed Incus proxies share the host's listener namespace, even
		// when their containers are stopped. Reserve both halves of our pair.
		for _, device := range record.Devices {
			if device["type"] != "proxy" || !strings.HasPrefix(device["listen"], "tcp:") {
				continue
			}
			_, number, err := net.SplitHostPort(strings.TrimPrefix(device["listen"], "tcp:"))
			if err != nil {
				return 0, fmt.Errorf("inspect existing proxy listener: %w", err)
			}
			port, err := strconv.ParseUint(number, 10, 16)
			if err != nil {
				return 0, fmt.Errorf("inspect existing proxy port: %w", err)
			}
			if port >= 40000 && port <= 49999 {
				port -= 10000
			}
			if port >= 30000 && port <= 39999 {
				used[uint16(port)] = true
			}
		}
	}
	for port := uint16(30000); port <= 39999; port++ {
		if used[port] {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		closeErr := listener.Close()
		if closeErr != nil {
			return 0, fmt.Errorf("release local VNC port: %w", closeErr)
		}
		return port, nil
	}
	return 0, fmt.Errorf("no VNC port is available in 30000-39999")
}

func (manager Manager) Create(name domain.Name, vncPort uint16, group string) error {
	if err := domain.ValidateGroup(group); err != nil {
		return err
	}
	incusName, err := incusInstanceName(name)
	if err != nil {
		return err
	}
	if vncPort < 30000 || vncPort > 39999 {
		return fmt.Errorf("VNC port %d is outside 30000-39999", vncPort)
	}
	record, inspectErr := manager.Inspect(name)
	if inspectErr != nil && !errors.Is(inspectErr, domain.ErrNotFound) {
		return inspectErr
	}
	devices := map[string]map[string]string{}
	if inspectErr == nil {
		if record.PendingOperation != "create" || record.VNCPort != vncPort || record.Group != group {
			return fmt.Errorf("container %q already exists or has conflicting preparation metadata", name)
		}
		output, err := manager.transport.Output([]string{"sudo", "incus", "query", "/1.0/instances/" + incusName})
		if err != nil {
			return fmt.Errorf("inspect pending container devices: %w", err)
		}
		var state struct {
			Devices map[string]map[string]string `json:"devices"`
		}
		if err := json.Unmarshal(output, &state); err != nil {
			return fmt.Errorf("decode pending container devices: %w", err)
		}
		devices = state.Devices
	} else {
		if err := manager.ensureImage(false); err != nil {
			return fmt.Errorf("prepare managed desktop image: %w", err)
		}
		if err := manager.runCommand([]string{
			"sudo", "incus", "init", managedImageAlias, incusName, "--storage", managedStoragePool,
			"--config", "boot.autostart=false", "--config", "user.plateau.managed=true",
			"--config", "user.plateau.name=" + name.String(), "--config", fmt.Sprintf("user.plateau.vnc-port=%d", vncPort),
			"--config", "user.plateau.desired=stopped", "--config", "user.plateau.pending=create",
			"--config", "user.plateau.group=" + group,
		}, nil, io.Discard, incusName, name.String()); err != nil {
			return fmt.Errorf("initialize Incus container %q: %w", name, err)
		}
	}
	for _, binding := range []struct {
		name   string
		port   int
		target int
	}{
		{"plateau-vnc", int(vncPort), 6080}, {"plateau-ssh", int(vncPort) + 10000, 22},
	} {
		listen := fmt.Sprintf("tcp:127.0.0.1:%d", binding.port)
		connect := fmt.Sprintf("tcp:127.0.0.1:%d", binding.target)
		if device, exists := devices[binding.name]; exists {
			if device["type"] != "proxy" || device["listen"] != listen || device["connect"] != connect {
				return fmt.Errorf("pending container %q has conflicting %s device", name, binding.name)
			}
			continue
		}
		if err := manager.runCommand([]string{"sudo", "incus", "config", "device", "add", incusName, binding.name, "proxy", "listen=" + listen, "connect=" + connect}, nil, io.Discard, incusName, name.String()); err != nil {
			// Keep the owned checkpoint. A later create resumes missing devices rather
			// than deleting data or pretending that the container never existed.
			return fmt.Errorf("configure %s for %q: %w", binding.name, name, err)
		}
	}
	return nil
}

func (manager Manager) Complete(name domain.Name) error {
	incusName, err := incusInstanceName(name)
	if err != nil {
		return err
	}
	return manager.runCommand([]string{"sudo", "incus", "config", "unset", incusName, "user.plateau.pending"}, nil, io.Discard, incusName, name.String())
}

func (manager Manager) Start(name domain.Name) error {
	incusName, err := incusInstanceName(name)
	if err != nil {
		return err
	}
	status, err := manager.Status(name)
	if err != nil {
		return err
	}
	if status != domain.StatusRunning {
		if err := manager.runCommand([]string{"sudo", "incus", "start", incusName}, nil, io.Discard, incusName, name.String()); err != nil {
			return err
		}
	}
	return nil
}

func (manager Manager) Stop(name domain.Name) error {
	incusName, err := incusInstanceName(name)
	if err != nil {
		return err
	}
	status, err := manager.Status(name)
	if err != nil {
		return err
	}
	if status == domain.StatusStopped {
		return nil
	}
	return manager.runCommand([]string{"sudo", "incus", "stop", incusName}, nil, io.Discard, incusName, name.String())
}

func (manager Manager) OpenVNC(port uint16) error {
	if port < 30000 || port > 39999 {
		return fmt.Errorf("VNC port %d is outside 30000-39999", port)
	}
	if err := manager.waitForVNC(fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/vnc.html?autoconnect=true&resize=scale&shared=1", port)
	return manager.openURL(url)
}

func (manager Manager) Delete(name domain.Name) error {
	incusName, err := incusInstanceName(name)
	if err != nil {
		return err
	}
	_, inspectErr := manager.Inspect(name)
	if inspectErr != nil && !errors.Is(inspectErr, domain.ErrNotFound) {
		return inspectErr
	}
	if inspectErr == nil {
		if err := manager.runCommand([]string{"sudo", "incus", "delete", incusName}, nil, io.Discard, incusName, name.String()); err != nil {
			return err
		}
	}

	if manager.sshDirectory != "" {
		if err := os.Remove(filepath.Join(manager.sshDirectory, "known_hosts-"+name.String())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("container %q is deleted; retry rm %s to remove its remembered SSH key: %w", name, name, err)
		}
	}
	return nil
}

func (manager Manager) EnableAutostart(name domain.Name) error {
	incusName, err := incusInstanceName(name)
	if err != nil {
		return err
	}
	return manager.runCommand([]string{"sudo", "incus", "config", "set", incusName, "boot.autostart=true", "user.plateau.desired=running"}, nil, io.Discard, incusName, name.String())
}

func (manager Manager) DisableAutostart(name domain.Name) error {
	incusName, err := incusInstanceName(name)
	if err != nil {
		return err
	}
	return manager.runCommand([]string{"sudo", "incus", "config", "set", incusName, "boot.autostart=false", "user.plateau.desired=stopped"}, nil, io.Discard, incusName, name.String())
}

func incusInstanceName(name domain.Name) (string, error) {
	parsed, err := domain.ParseName(name.String())
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func (manager Manager) SetGroup(name domain.Name, group string) error {
	if err := domain.ValidateGroup(group); err != nil {
		return err
	}
	record, err := manager.Inspect(name)
	if err != nil {
		return err
	}
	if record.PendingOperation != "" || (record.RuntimeStatus != domain.StatusRunning && record.RuntimeStatus != domain.StatusStopped) {
		return fmt.Errorf("%w: container %q must finish its pending operation before changing groups", domain.ErrInvalidState, name)
	}
	return manager.runCommand([]string{"sudo", "incus", "config", "set", name.String(), "user.plateau.group=" + group}, nil, io.Discard, name.String(), name.String())
}
