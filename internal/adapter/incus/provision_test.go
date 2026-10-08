package incus

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopSetupPreservesUserConfiguration(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"usr/local/bin", "etc/systemd/system", "etc/profile.d", "home/plateau"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Execute real shell file creation against an isolated filesystem. Ownership
	// changes alone are omitted because the macOS test user is not "plateau".
	script := strings.NewReplacer(
		"/usr/local/bin", root+"/usr/local/bin",
		"/etc/systemd/system", root+"/etc/systemd/system",
		"/etc/profile.d", root+"/etc/profile.d",
		"/etc/plateau", root+"/etc/plateau",
		"/etc/xdg", root+"/etc/xdg",
		"/usr/share/xfce4", root+"/usr/share/xfce4",
		"/home/plateau", root+"/home/plateau",
		"install -d -o plateau -g plateau", "mkdir -p",
		"chown plateau:plateau", "true",
	).Replace(containerDesktopSetupScript)
	paths := []string{
		".profile", ".config/openbox/autostart", ".config/openbox/menu.xml",
		".config/tint2/tint2rc", ".local/share/applications/plateau-terminal.desktop",
		".local/share/applications/plateau-browser.desktop",
		".config/fcitx5/config", ".config/fcitx5/profile",
		".config/xfce4/helpers.rc", ".config/mimeapps.list",
	}
	resources := filepath.Join(root, "home/plateau/.Xresources")
	if err := os.WriteFile(resources, []byte("XTerm*foreground: green\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "test-bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"xrdb":                               "#!/bin/sh\ncat \"$2\"\n",
		"openbox-session":                    "#!/bin/sh\nprintf '%s\\n' \"$DBUS_SESSION_BUS_ADDRESS\" >\"$HOME/test-session-bus\"\n",
		"fcitx5":                             "#!/bin/sh\nexit 0\n",
		"dbus-update-activation-environment": "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$HOME/test-activation-environment\"\n",
		"update-desktop-database":            "#!/bin/sh\nexit 0\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for pass := 0; pass < 2; pass++ {
		command := exec.Command("/bin/bash", "-eu")
		command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		command.Stdin = strings.NewReader(script)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("setup: %v\n%s", err, output)
		}
		session := exec.Command(filepath.Join(root, "usr/local/bin/plateau-session"))
		session.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "HOME="+filepath.Join(root, "home/plateau"), "XDG_RUNTIME_DIR=/run/user/1000", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/obsolete-private-bus")
		output, err := session.CombinedOutput()
		if err != nil {
			t.Fatalf("desktop session: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "copy-selection(CLIPBOARD)") || !strings.Contains(string(output), "insert-selection(CLIPBOARD)") || !strings.HasSuffix(string(output), "XTerm*foreground: green\n") {
			t.Fatalf("session must load clipboard bindings followed by user resources: %s", output)
		}
		bus, err := os.ReadFile(filepath.Join(root, "home/plateau/test-session-bus"))
		if err != nil || string(bus) != "unix:path=/run/user/1000/bus\n" {
			t.Fatalf("desktop must use the user service bus: %q, %v", bus, err)
		}
		activation, err := os.ReadFile(filepath.Join(root, "home/plateau/test-activation-environment"))
		if err != nil || string(activation) != "--systemd\nDISPLAY\nGTK_IM_MODULE\nQT_IM_MODULE\nXMODIFIERS\n" {
			t.Fatalf("desktop activation environment: %q, %v", activation, err)
		}
		if pass == 0 {
			browser := filepath.Join(root, "home/plateau/.local/share/applications/plateau-browser.desktop")
			browserData, err := os.ReadFile(browser)
			if err != nil || !strings.Contains(string(browserData), "Exec=plateau-browser %U\n") {
				t.Fatalf("browser must accept link arguments: %q, %v", browserData, err)
			}
			if err := os.WriteFile(browser, []byte(strings.Replace(string(browserData), "Exec=plateau-browser %U", "Exec=plateau-browser", 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(root, "home/plateau/.local/share/applications/plateau-terminal.desktop")
			data, err := os.ReadFile(launcher)
			if err != nil || !strings.Contains(string(data), "Exec=plateau-terminal\n") {
				t.Fatalf("default terminal launcher: %q, %v", data, err)
			}
			legacy := strings.Replace(string(data), "Exec=plateau-terminal", `Exec=xterm -fa "Noto Sans Mono" -fs 12`, 1)
			if err := os.WriteFile(launcher, []byte(legacy), 0o644); err != nil {
				t.Fatal(err)
			}
			command = exec.Command("/bin/bash", "-eu")
			command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			command.Stdin = strings.NewReader(script)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("upgrade legacy terminal: %v\n%s", err, output)
			}
			data, err = os.ReadFile(launcher)
			if err != nil || !strings.Contains(string(data), "Exec=plateau-terminal\n") {
				t.Fatalf("legacy terminal was not upgraded: %q, %v", data, err)
			}
			browserData, err = os.ReadFile(browser)
			if err != nil || !strings.Contains(string(browserData), "Exec=plateau-browser %U\n") {
				t.Fatalf("legacy browser does not accept link arguments: %q, %v", browserData, err)
			}
		}
		for _, relative := range paths {
			path := filepath.Join(root, "home/plateau", relative)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if pass == 1 && string(data) != "user customization\n" {
				t.Errorf("setup overwrote %s", relative)
			}
			if err := os.WriteFile(path, []byte("user customization\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestProvisioningScriptsHaveValidBashSyntax(t *testing.T) {
	for name, script := range map[string]string{
		"create":       containerProvisionScript,
		"update":       containerUpdateScript,
		"image update": containerManagedImageUpdateScript,
		"SSH repair":   containerSSHRepairScript,
	} {
		t.Run(name, func(t *testing.T) {
			command := exec.Command("/bin/bash", "-n")
			command.Stdin = strings.NewReader(script)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("invalid shell syntax: %v\n%s", err, output)
			}
		})
	}
}

func TestSSHSetupEnablesPasswordlessSudoWithoutChangingOtherRules(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc/systemd/system"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc/sudoers.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "etc/sudoers.d/other")
	if err := os.WriteFile(other, []byte("root ALL=(ALL:ALL) ALL\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	script := strings.NewReplacer(
		"/etc/", root+"/etc/",
		"usermod --password '*' plateau", "true",
		"systemctl", "true",
		"visudo", "true",
	).Replace(containerSSHSetupScript)
	for pass := 0; pass < 2; pass++ {
		// 실제 설정은 root로 실행됩니다. macOS 일반 사용자 테스트에서는 쓰기 권한만 보완합니다.
		if pass > 0 {
			if err := os.Chmod(filepath.Join(root, "etc/sudoers.d/90-plateau"), 0o640); err != nil {
				t.Fatal(err)
			}
		}
		command := exec.Command("/bin/bash", "-eu")
		command.Stdin = strings.NewReader(script)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("setup: %v\n%s", err, output)
		}
		path := filepath.Join(root, "etc/sudoers.d/90-plateau")
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "plateau ALL=(ALL:ALL) NOPASSWD: ALL\n" {
			t.Fatalf("sudo policy = %q, %v", data, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o440 {
			t.Fatalf("sudo policy permissions: %v, %v", info, err)
		}
		data, err = os.ReadFile(other)
		if err != nil || string(data) != "root ALL=(ALL:ALL) ALL\n" {
			t.Fatalf("other sudo policy changed: %q, %v", data, err)
		}
	}
}
