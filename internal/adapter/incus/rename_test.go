package incus

import (
	"encoding/json"
	"errors"
	"github.com/creatrip/plateau/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenameResumesEachCheckpoint(t *testing.T) {
	for _, phase := range []string{"initial", "marked", "moved", "complete"} {
		t.Run(phase, func(t *testing.T) {
			manager, transport := newManagerTest(t)
			config := map[string]string{"user.plateau.managed": "true", "user.plateau.name": "old", "user.plateau.vnc-port": "30000", "boot.autostart": "false", "user.plateau.group": "업무"}
			actual := "old"
			if phase != "initial" {
				config["user.plateau.pending"] = "rename"
				config["user.plateau.rename-from"] = "old"
				config["user.plateau.rename-to"] = "new"
				config["user.plateau.rename-hosts"] = `"127.0.1.1\tnew\n127.0.0.1\tlocalhost\n"`
			}
			if phase == "moved" || phase == "complete" {
				actual = "new"
			}
			if phase == "complete" {
				config["user.plateau.name"] = "new"
				config["user.plateau.pending"] = ""
				config["user.plateau.renamed-from"] = "old"
				delete(config, "user.plateau.rename-from")
				delete(config, "user.plateau.rename-to")
			}
			data, err := json.Marshal([]any{map[string]any{"name": actual, "type": "container", "status": "Stopped", "config": config}})
			if err != nil {
				t.Fatal(err)
			}
			transport.outputs["sudo\x00incus\x00list\x00^"+actual+"$\x00--format=json"] = data
			for _, name := range []string{"old", "new", "unrelated"} {
				if err := os.WriteFile(filepath.Join(manager.sshDirectory, "known_hosts-"+name), []byte("key"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := manager.Rename("old", "new"); err != nil {
				t.Fatal(err)
			}
			moves := 0
			wroteRecordedHosts := false
			for _, call := range transport.calls {
				command := strings.Join(call.args, " ")
				if strings.HasPrefix(command, "sudo incus file push - new/etc/hosts") && call.stdin == "127.0.1.1\tnew\n127.0.0.1\tlocalhost\n" {
					wroteRecordedHosts = true
				}
				if command == "sudo incus move old new" {
					moves++
				}
				if strings.Contains(command, "delete") || strings.Contains(command, "user.plateau.group=") || strings.Contains(command, "user.plateau.vnc-port=") {
					t.Fatalf("unrelated mutation: %s", command)
				}
			}
			if (phase == "marked" || phase == "moved") && !wroteRecordedHosts {
				t.Fatal("retry failed to restore checkpointed hosts content")
			}
			wantMoves := 0
			if phase == "initial" || phase == "marked" {
				wantMoves = 1
			}
			if moves != wantMoves {
				t.Fatalf("moves=%d want=%d", moves, wantMoves)
			}
			if phase != "complete" {
				for _, name := range []string{"old", "new"} {
					if _, err := os.Stat(filepath.Join(manager.sshDirectory, "known_hosts-"+name)); !os.IsNotExist(err) {
						t.Fatalf("stale key remains: %s %v", name, err)
					}
				}
				last := strings.Join(transport.calls[len(transport.calls)-1].args, " ")
				if !strings.Contains(last, "config set new user.plateau.name=new user.plateau.pending=") {
					t.Fatal(last)
				}
			}
			if _, err := os.Stat(filepath.Join(manager.sshDirectory, "known_hosts-unrelated")); err != nil {
				t.Fatal("unrelated SSH record removed")
			}
		})
	}
}

func TestRenameRejectsConflictingOrUnreadyContainers(t *testing.T) {
	for _, problem := range []string{"running", "autostart", "unmanaged", "invalid", "create", "target", "unmanaged-target", "missing", "same", "bad-name", "wrong-checkpoint"} {
		t.Run(problem, func(t *testing.T) {
			manager, transport := newManagerTest(t)
			config := map[string]string{"user.plateau.managed": "true", "user.plateau.name": "old", "user.plateau.vnc-port": "30000", "boot.autostart": "false"}
			status := "Stopped"
			switch problem {
			case "running":
				status = "Running"
			case "autostart":
				config["boot.autostart"] = "true"
			case "unmanaged":
				delete(config, "user.plateau.managed")
			case "invalid":
				config["user.plateau.name"] = "other"
			case "create":
				config["user.plateau.pending"] = "create"
			case "wrong-checkpoint":
				config["user.plateau.pending"] = "rename"
				config["user.plateau.rename-from"] = "old"
				config["user.plateau.rename-to"] = "other"
			}
			data, err := json.Marshal([]any{map[string]any{"name": "old", "type": "container", "status": status, "config": config}})
			if err != nil {
				t.Fatal(err)
			}
			if problem != "missing" {
				transport.outputs["sudo\x00incus\x00list\x00^old$\x00--format=json"] = data
			}
			if problem == "target" || problem == "unmanaged-target" {
				config["user.plateau.name"] = "new"
				if problem == "unmanaged-target" {
					delete(config, "user.plateau.managed")
				}
				data, err = json.Marshal([]any{map[string]any{"name": "new", "type": "container", "status": "Stopped", "config": config}})
				if err != nil {
					t.Fatal(err)
				}
				transport.outputs["sudo\x00incus\x00list\x00^new$\x00--format=json"] = data
			}
			target := domain.Name("new")
			if problem == "same" {
				target = "old"
			}
			if problem == "bad-name" {
				target = "../bad"
			}
			if err := manager.Rename("old", target); err == nil {
				t.Fatal("unsafe rename accepted")
			}
			for _, call := range transport.calls {
				if len(call.args) < 3 || call.args[2] != "list" {
					t.Fatalf("mutated on error: %+v", call)
				}
			}
		})
	}
}

func TestRenameFailureRetainsRetryCheckpoint(t *testing.T) {
	for _, phase := range []string{"mark", "move", "hostname", "hosts", "cleanup", "finish"} {
		t.Run(phase, func(t *testing.T) {
			manager, transport := newManagerTest(t)
			transport.outputs["sudo\x00incus\x00list\x00^old$\x00--format=json"] = []byte(`[{"name":"old","type":"container","status":"Stopped","config":{"user.plateau.managed":"true","user.plateau.name":"old","user.plateau.vnc-port":"30000","boot.autostart":"false"}}]`)
			failure := errors.New("injected disconnect")
			commands := map[string]string{
				"mark":     "sudo incus config set old user.plateau.pending=rename user.plateau.rename-from=old user.plateau.rename-to=new user.plateau.rename-hosts=\"\"",
				"move":     "sudo incus move old new",
				"hostname": "sudo incus file push - new/etc/hostname --uid 0 --gid 0 --mode 0644",
				"hosts":    "sudo incus file push - new/etc/hosts --uid 0 --gid 0 --mode 0644",
				"finish":   "sudo incus config set new user.plateau.name=new user.plateau.pending= user.plateau.rename-from= user.plateau.rename-to= user.plateau.rename-hosts= user.plateau.renamed-from=old",
			}
			if phase == "cleanup" {
				dir := filepath.Join(manager.sshDirectory, "known_hosts-old")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "keep"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				transport.runErrors = map[string]error{commands[phase]: failure}
			}
			err := manager.Rename("old", "new")
			if err == nil || (phase != "cleanup" && !errors.Is(err, failure)) {
				t.Fatalf("failure lost: %v", err)
			}
			for _, call := range transport.calls {
				if strings.Contains(strings.Join(call.args, " "), "incus delete") {
					t.Fatal("failure removed data")
				}
			}
			if phase == "cleanup" {
				last := strings.Join(transport.calls[len(transport.calls)-1].args, " ")
				if last != "sudo incus file push - new/etc/hosts --uid 0 --gid 0 --mode 0644" {
					t.Fatalf("checkpoint cleared before SSH cleanup: %s", last)
				}
			}
		})
	}
}

func TestRenameUpdatesGuestHostnameAndPreservesOtherHostEntries(t *testing.T) {
	manager, transport := newManagerTest(t)
	transport.outputs["sudo\x00incus\x00list\x00^old$\x00--format=json"] = []byte(`[{"name":"old","type":"container","status":"Stopped","config":{"user.plateau.managed":"true","user.plateau.name":"old","user.plateau.vnc-port":"30000","boot.autostart":"false"}}]`)
	transport.outputs["sudo\x00incus\x00file\x00pull\x00old/etc/hosts\x00-"] = []byte("127.0.1.1 old alias # old comment\n127.0.0.1 localhost\n10.0.0.5 old-api\n")
	want := map[string]string{"new/etc/hostname": "new\n", "new/etc/hosts": "127.0.1.1\tnew\talias # old comment\n127.0.0.1 localhost\n10.0.0.5 old-api\n"}
	if err := manager.Rename("old", "new"); err != nil {
		t.Fatal(err)
	}
	for _, call := range transport.calls {
		if len(call.args) > 5 && call.args[2] == "file" && call.args[3] == "push" {
			if expected, ok := want[call.args[5]]; !ok || call.stdin != expected {
				t.Fatalf("unexpected file write: %+v", call)
			}
			delete(want, call.args[5])
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing guest updates: %v", want)
	}
}

func TestInventoryRejectsMalformedRenameCheckpoints(t *testing.T) {
	for _, field := range []string{"user.plateau.rename-from", "user.plateau.rename-to", "user.plateau.rename-hosts", "user.plateau.name", "boot.autostart", "user.plateau.vnc-port", "user.plateau.group", "status"} {
		t.Run(field, func(t *testing.T) {
			manager, transport := newManagerTest(t)
			config := map[string]string{"user.plateau.managed": "true", "user.plateau.name": "old", "user.plateau.vnc-port": "30000", "boot.autostart": "false", "user.plateau.pending": "rename", "user.plateau.rename-from": "old", "user.plateau.rename-to": "new", "user.plateau.rename-hosts": `""`}
			config[field] = "../invalid"
			if field == "user.plateau.group" {
				config[field] = "bad\nlabel"
			}
			status := "Stopped"
			if field == "status" {
				status = "Running"
				delete(config, field)
			}
			data, err := json.Marshal([]any{map[string]any{"name": "new", "type": "container", "status": status, "config": config}})
			if err != nil {
				t.Fatal(err)
			}
			transport.outputs["sudo\x00incus\x00list\x00--format=json"] = data
			if _, err := manager.List(); !errors.Is(err, domain.ErrInvalidState) {
				t.Fatalf("invalid checkpoint accepted: %v", err)
			}
		})
	}
}
