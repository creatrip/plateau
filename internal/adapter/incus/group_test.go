package incus

import (
	"encoding/json"
	"errors"
	"github.com/creatrip/plateau/internal/domain"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetGroupUsesOwnedReadyContainerWithoutRestarting(t *testing.T) {
	for _, state := range []string{"Running", "Stopped"} {
		for _, group := range []string{"업무 자동화", ""} {
			manager, transport := newManagerTest(t)
			transport.outputs["sudo\x00incus\x00list\x00^a$\x00--format=json"] = []byte(`[{"name":"a","type":"container","status":"` + state + `","config":{"user.plateau.managed":"true","user.plateau.name":"a","user.plateau.vnc-port":"30000","boot.autostart":"true","user.plateau.group":"이전"}}]`)
			if err := manager.SetGroup("a", group); err != nil {
				t.Fatal(err)
			}
			if len(transport.calls) != 2 || strings.Join(transport.calls[1].args, " ") != "sudo incus config set a user.plateau.group="+group {
				t.Fatalf("unexpected mutations: %+v", transport.calls)
			}
		}
	}
	for _, pending := range []string{"create", "restore"} {
		manager, transport := newManagerTest(t)
		transport.outputs["sudo\x00incus\x00list\x00^a$\x00--format=json"] = []byte(`[{"name":"a","type":"container","status":"Stopped","config":{"user.plateau.managed":"true","user.plateau.name":"a","user.plateau.vnc-port":"30000","boot.autostart":"false","user.plateau.pending":"` + pending + `","user.plateau.restore-id":"` + strings.Repeat("a", 64) + `"}}]`)
		if err := manager.SetGroup("a", "new"); !errors.Is(err, domain.ErrInvalidState) {
			t.Fatalf("pending=%s err=%v", pending, err)
		}
		if len(transport.calls) != 1 {
			t.Fatal("modified pending container")
		}
	}
}

func TestInventoryGroupsValidateUntrustedMetadata(t *testing.T) {
	for _, group := range []string{"", "업무", "bad\nlabel"} {
		manager, transport := newManagerTest(t)
		record := map[string]any{"name": "a", "type": "container", "status": "Stopped", "config": map[string]string{"user.plateau.managed": "true", "user.plateau.name": "a", "user.plateau.vnc-port": "30000", "boot.autostart": "false", "user.plateau.group": group}}
		data, err := json.Marshal([]any{record})
		if err != nil {
			t.Fatal(err)
		}
		transport.outputs["sudo\x00incus\x00list\x00--format=json"] = data
		records, err := manager.List()
		if group == "bad\nlabel" {
			if !errors.Is(err, domain.ErrInvalidState) {
				t.Fatal(err)
			}
			continue
		}
		if err != nil || len(records) != 1 || records[0].Group != group {
			t.Fatalf("records=%+v err=%v", records, err)
		}
	}
}

func TestGroupBackupCompatibilityAndManifestBinding(t *testing.T) {
	for _, group := range []string{"", "업무 자동화"} {
		manager, transport := newManagerTest(t)
		instance := domain.Instance{Name: "a", Group: group, VNCPort: 30000, DesiredState: domain.DesiredStopped}
		transport.remoteFile = incusExportPayload(t, instance)
		bundle := filepath.Join(t.TempDir(), "backup.plateau")
		if err := manager.Backup(instance, bundle); err != nil {
			t.Fatal(err)
		}
		got, err := manager.ReadBackupManifest(bundle)
		if err != nil || got != instance {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		if _, err := readPlateauBundle(bundle, filepath.Join(t.TempDir(), "import.tar.gz")); err != nil {
			t.Fatal(err)
		}
		changed := instance
		changed.Group = "different"
		before := len(transport.calls)
		if err := manager.RestoreBackup(bundle, changed); err == nil {
			t.Fatal("changed manifest accepted")
		}
		if len(transport.calls) != before {
			t.Fatal("modified host for changed manifest")
		}
		// A valid payload checksum must not permit divergent wrapper and Incus groups.
		other := filepath.Join(t.TempDir(), "mismatch.plateau")
		if err := manager.Backup(changed, other); err != nil {
			t.Fatal(err)
		}
		if _, err := readPlateauBundle(other, ""); err == nil {
			t.Fatal("divergent group metadata accepted")
		}
	}
}
