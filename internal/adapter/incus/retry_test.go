package incus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creatrip/plateau/internal/domain"
)

func TestCreateResumesOnlyMissingDevices(t *testing.T) {
	manager, transport := newManagerTest(t)
	transport.outputs["sudo\x00incus\x00list\x00^a$\x00--format=json"] = []byte(`[{"name":"a","type":"container","status":"Stopped","config":{"user.plateau.managed":"true","user.plateau.name":"a","user.plateau.vnc-port":"30000","boot.autostart":"true","user.plateau.pending":"create"}}]`)
	transport.outputs["sudo\x00incus\x00query\x00/1.0/instances/a"] = []byte(`{"devices":{"plateau-vnc":{"type":"proxy","listen":"tcp:127.0.0.1:30000","connect":"tcp:127.0.0.1:6080"}}}`)
	if err := manager.Create("a", 30000, ""); err != nil {
		t.Fatal(err)
	}
	for _, call := range transport.calls {
		command := strings.Join(call.args, " ")
		if strings.Contains(command, "incus init") || strings.Contains(command, "incus delete") || strings.Contains(command, "device add a plateau-vnc") {
			t.Fatalf("retry repeated completed/destructive work: %s", command)
		}
	}
	last := strings.Join(transport.calls[len(transport.calls)-1].args, " ")
	if !strings.Contains(last, "device add a plateau-ssh") {
		t.Fatalf("missing SSH repair: %s", last)
	}
}

func TestRestoreResumesStagingAtEveryPublicationBoundary(t *testing.T) {
	for _, phase := range []string{"post-import", "rebind", "checkpoint", "publish"} {
		t.Run(phase, func(t *testing.T) {
			manager, transport := newManagerTest(t)
			instance := domain.Instance{Name: "a", VNCPort: 30000, DesiredState: domain.DesiredRunning}
			transport.remoteFile = incusExportPayload(t, instance)
			path := filepath.Join(t.TempDir(), "backup.plateau")
			if err := manager.Backup(instance, path); err != nil {
				t.Fatal(err)
			}
			manifest, err := readPlateauBundle(path, "")
			if err != nil {
				t.Fatal(err)
			}
			staging := "plateau-import-" + manifest.sourceID[:24]
			key := "sudo\x00incus\x00list\x00^" + staging + "$\x00--format=json"
			staged := []byte(fmt.Sprintf(`[{"name":%q,"type":"container","status":"Stopped","config":{"user.plateau.managed":"true","user.plateau.name":"a","user.plateau.desired":"running","user.plateau.vnc-port":"30000","boot.autostart":"false","user.plateau.pending":"restore","user.plateau.restore-id":%q},"devices":{"plateau-vnc":{"type":"proxy","connect":"tcp:127.0.0.1:6080"}}}]`, staging, manifest.sourceID))
			transport.outputs[key] = staged
			transport.outputSequences = map[string][][]byte{key: {[]byte("[]"), staged}}
			commands := map[string]string{
				"rebind":     "sudo incus config device set " + staging + " plateau-vnc listen=tcp:127.0.0.1:30000",
				"checkpoint": "sudo incus config set " + staging + " user.plateau.vnc-port=30000 user.plateau.pending=restore user.plateau.restore-id=" + manifest.sourceID + " boot.autostart=false",
				"publish":    "sudo incus move " + staging + " a",
			}
			if phase == "post-import" {
				transport.outputSequences[key][1] = []byte("broken response")
			} else {
				transport.runErrors = map[string]error{commands[phase]: errors.New("injected failure")}
			}
			if err := manager.RestoreBackup(path, instance); err == nil {
				t.Fatal("failure was swallowed")
			}
			transport.runErrors = nil
			if err := manager.RestoreBackup(path, instance); err != nil {
				t.Fatal(err)
			}
			imports := 0
			for _, call := range transport.calls {
				command := strings.Join(call.args, " ")
				if strings.HasPrefix(command, "sudo incus import ") {
					imports++
				}
				if strings.Contains(command, "incus delete") {
					t.Fatalf("failed inspection deleted data: %s", command)
				}
			}
			if imports != 1 {
				t.Fatalf("reimported completed payload %d times", imports)
			}
			transport.outputs["sudo\x00incus\x00list\x00--format=json"] = staged
			if records, err := manager.List(); err != nil || len(records) != 0 {
				t.Fatalf("exposed staging: %+v %v", records, err)
			}
		})
	}
}

func TestPublishedRestoreOnlyResumesTheSameArchive(t *testing.T) {
	manager, transport := newManagerTest(t)
	instance := domain.Instance{Name: "a", VNCPort: 30000, DesiredState: domain.DesiredRunning}
	transport.remoteFile = incusExportPayload(t, instance)
	path := filepath.Join(t.TempDir(), "backup.plateau")
	if err := manager.Backup(instance, path); err != nil {
		t.Fatal(err)
	}
	manifest, err := readPlateauBundle(path, "")
	if err != nil {
		t.Fatal(err)
	}
	key := "sudo\x00incus\x00list\x00^a$\x00--format=json"
	for _, source := range []string{manifest.sourceID, strings.Repeat("0", 64)} {
		transport.outputs[key] = []byte(fmt.Sprintf(`[{"name":"a","type":"container","status":"Stopped","config":{"user.plateau.managed":"true","user.plateau.name":"a","user.plateau.vnc-port":"30000","boot.autostart":"false","user.plateau.pending":"restore","user.plateau.restore-id":%q}}]`, source))
		err := manager.RestoreBackup(path, instance)
		if (err == nil) != (source == manifest.sourceID) {
			t.Fatalf("source=%s err=%v", source, err)
		}
	}
}

func TestSSHIdentityRecoversMissingPublicFile(t *testing.T) {
	manager, transport := newManagerTest(t)
	transport.outputs["sudo\x00incus\x00query\x00/1.0/instances/a"] = []byte(`{"config":{"user.plateau.managed":"true","user.plateau.name":"a"},"devices":{}}`)
	if err := manager.PrepareSSH("a", 30000); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(manager.sshDirectory, "id_ed25519.pub")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := manager.PrepareSSH("a", 30000); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(strings.Fields(string(before))[:2], " ") != strings.Join(strings.Fields(string(after))[:2], " ") {
		t.Fatal("private identity was replaced during repair")
	}
}
