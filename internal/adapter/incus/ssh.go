package incus

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/creatrip/plateau/internal/domain"
)

func (manager Manager) PrepareSSH(name domain.Name, vncPort uint16) error {
	incusName, err := incusInstanceName(name)
	if err != nil {
		return err
	}
	if vncPort < 30000 || vncPort > 39999 {
		return fmt.Errorf("VNC port %d is outside 30000-39999", vncPort)
	}
	sshPort := uint16(uint32(vncPort) + 10000)
	output, err := manager.transport.Output([]string{"sudo", "incus", "query", "/1.0/instances/" + incusName})
	if err != nil {
		return fmt.Errorf("inspect Incus SSH proxy for %q: %w", name, err)
	}
	var instance struct {
		Config  map[string]string            `json:"config"`
		Devices map[string]map[string]string `json:"devices"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(&instance); err != nil {
		return fmt.Errorf("decode Incus SSH proxy for %q: %w", name, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("decode Incus SSH proxy for %q: trailing data", name)
	}
	if instance.Config["user.plateau.managed"] != "true" || instance.Config["user.plateau.name"] != name.String() {
		return fmt.Errorf("Incus container %q is not managed by Plateau", name)
	}
	sshDevice, exists := instance.Devices["plateau-ssh"]
	if !exists {
		if err := manager.runCommand([]string{
			"sudo", "incus", "config", "device", "add", incusName, "plateau-ssh", "proxy",
			fmt.Sprintf("listen=tcp:127.0.0.1:%d", sshPort), "connect=tcp:127.0.0.1:22",
		}, nil, io.Discard, incusName, name.String()); err != nil {
			return fmt.Errorf("configure Incus SSH proxy for %q: %w", name, err)
		}
	} else if sshDevice["type"] != "proxy" || sshDevice["listen"] != fmt.Sprintf("tcp:127.0.0.1:%d", sshPort) || sshDevice["connect"] != "tcp:127.0.0.1:22" {
		return fmt.Errorf("managed SSH proxy for %q has unexpected configuration", name)
	}
	if manager.sshDirectory == "" {
		return fmt.Errorf("locate Plateau SSH configuration directory")
	}
	// A shared identity is initialized once across all container preparations.
	// Release before any guest work; different containers need not wait on SSH.
	identityDone, err := manager.locks.Acquire("identity")
	if err != nil {
		return err
	}
	defer identityDone()
	if err := os.MkdirAll(manager.sshDirectory, 0o700); err != nil {
		return fmt.Errorf("create Plateau SSH configuration directory: %w", err)
	}
	if err := os.Chmod(manager.sshDirectory, 0o700); err != nil {
		return fmt.Errorf("secure Plateau SSH configuration directory: %w", err)
	}
	identityPath := filepath.Join(manager.sshDirectory, "id_ed25519")
	privateInfo, privateErr := os.Lstat(identityPath)
	publicInfo, publicErr := os.Lstat(identityPath + ".pub")
	if os.IsNotExist(privateErr) && os.IsNotExist(publicErr) {
		temporary, err := os.MkdirTemp(manager.sshDirectory, "identity-*")
		if err != nil {
			return fmt.Errorf("stage SSH identity: %w", err)
		}
		defer os.RemoveAll(temporary)
		staged := filepath.Join(temporary, "id_ed25519")
		if err := manager.runner.Run(manager.sshKeygenPath, []string{"-q", "-t", "ed25519", "-N", "", "-C", "plateau", "-f", staged}, nil, io.Discard, manager.stderr); err != nil {
			return fmt.Errorf("generate Plateau SSH identity: %w", err)
		}
		if err := os.Rename(staged, identityPath); err != nil {
			return fmt.Errorf("publish SSH private key: %w", err)
		}
		if err := os.Rename(staged+".pub", identityPath+".pub"); err != nil {
			return fmt.Errorf("publish SSH public key: %w", err)
		}
		privateInfo, privateErr = os.Lstat(identityPath)
		publicInfo, publicErr = os.Lstat(identityPath + ".pub")
	}
	// A crash between the two renames must not replace a private key that may
	// already be authorized in other containers. Derive its missing public half.
	if privateErr == nil && privateInfo.Mode().IsRegular() && os.IsNotExist(publicErr) {
		output, err := manager.runner.Output(manager.sshKeygenPath, []string{"-y", "-P", "", "-f", identityPath})
		if err != nil {
			return fmt.Errorf("recover SSH public key: %w", err)
		}
		if err := os.WriteFile(identityPath+".pub", output, 0o644); err != nil {
			return fmt.Errorf("save recovered SSH public key: %w", err)
		}
		publicInfo, publicErr = os.Lstat(identityPath + ".pub")
	}
	if privateErr != nil || publicErr != nil {
		return fmt.Errorf("validate Plateau SSH identity: private key and public key must both exist")
	}
	if !privateInfo.Mode().IsRegular() || !publicInfo.Mode().IsRegular() {
		return fmt.Errorf("validate Plateau SSH identity: keys must be regular files")
	}
	if err := os.Chmod(identityPath, 0o600); err != nil {
		return fmt.Errorf("secure Plateau SSH private key: %w", err)
	}
	if err := os.Chmod(identityPath+".pub", 0o644); err != nil {
		return fmt.Errorf("secure Plateau SSH public key: %w", err)
	}
	publicKeyBytes, err := os.ReadFile(identityPath + ".pub")
	if err != nil {
		return fmt.Errorf("read Plateau SSH public key: %w", err)
	}
	publicKey := strings.TrimSpace(string(publicKeyBytes))
	if !strings.HasPrefix(publicKey, "ssh-ed25519 ") || strings.ContainsAny(publicKey, "\r\n") {
		return fmt.Errorf("validate Plateau SSH public key")
	}
	identityDone()
	readyDigest := sha256.Sum256([]byte(containerSSHReadyVersion + "\n" + publicKey))
	readyValue := fmt.Sprintf("%x", readyDigest)
	if instance.Config["user.plateau.ssh-ready"] != readyValue {
		var repairErrors bytes.Buffer
		if err := manager.transport.Run([]string{"sudo", "incus", "exec", incusName, "--", "bash", "-s", "--", publicKey}, strings.NewReader(containerSSHRepairScript), io.Discard, &repairErrors); err != nil {
			message := strings.TrimSpace(repairErrors.String())
			if message != "" {
				return fmt.Errorf("prepare container SSH for %q: %w: %s", name, err, message)
			}
			return fmt.Errorf("prepare container SSH for %q: %w", name, err)
		}
		if err := os.Remove(filepath.Join(manager.sshDirectory, "known_hosts-"+name.String())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale SSH host key for %q: %w", name, err)
		}
		if err := manager.runCommand([]string{"sudo", "incus", "config", "set", incusName, "user.plateau.ssh-ready=" + readyValue}, nil, io.Discard, incusName, name.String()); err != nil {
			return fmt.Errorf("record container SSH readiness for %q: %w", name, err)
		}
	}
	return nil
}

func (manager Manager) SSH(name domain.Name, vncPort uint16) error {
	if _, err := incusInstanceName(name); err != nil {
		return err
	}
	if vncPort < 30000 || vncPort > 39999 {
		return fmt.Errorf("invalid SSH port allocation")
	}
	sshPort := uint16(uint32(vncPort) + 10000)
	identityPath := filepath.Join(manager.sshDirectory, "id_ed25519")
	limactlPath, err := exec.LookPath("limactl")
	if err != nil {
		return fmt.Errorf("locate managed Lima executable: %w", err)
	}
	if !filepath.IsAbs(limactlPath) {
		return fmt.Errorf("managed Lima executable path is not absolute")
	}
	quotedLimactlPath := "'" + strings.ReplaceAll(limactlPath, "'", "'\"'\"'") + "'"
	knownHostsPath := filepath.Join(manager.sshDirectory, "known_hosts-"+name.String())
	arguments := []string{
		"-F", "/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "PreferredAuthentications=publickey",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "StrictHostKeyChecking=accept-new",
		// OpenSSH는 -o 값을 다시 해석하므로 공백이 있는 경로를 인용합니다.
		"-o", fmt.Sprintf("UserKnownHostsFile=%q", knownHostsPath),
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", fmt.Sprintf("ProxyCommand=exec %s shell --tty=false plateau-host -- /usr/bin/nc 127.0.0.1 %d", quotedLimactlPath, sshPort),
		"-i", identityPath,
		"plateau@" + name.String(),
	}
	runner := manager.runner
	runner.Interactive = true
	if err := runner.Run(manager.sshPath, arguments, manager.stdin, manager.stdout, manager.stderr); err != nil {
		return fmt.Errorf("connect to container SSH for %q: %w", name, err)
	}
	return nil
}
