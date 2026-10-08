package application_test

import (
	"errors"
	"github.com/creatrip/plateau/internal/application"
	"github.com/creatrip/plateau/internal/domain"
	"reflect"
	"testing"
)

func TestRenameLocksBothNamesInStableOrderAndReleasesOnFailure(t *testing.T) {
	for _, fail := range []string{"", "lock", "runtime"} {
		for _, names := range [][2]string{{"a", "z"}, {"z", "a"}} {
			runtime := newRuntimeFake()
			locks := &lockFake{}
			called := false
			runtime.onRename = func() {
				called = true
				if !locks.held["container-a"] || !locks.held["container-z"] {
					t.Fatal("rename did not hold both locks")
				}
			}
			if fail == "lock" {
				locks.failResource = "container-z"
			}
			if fail == "runtime" {
				runtime.faults["rename"] = errors.New("rename failed")
			}
			err := application.NewInstances(runtime, locks).Rename(names[0], names[1])
			if (err == nil) != (fail == "") || called != (fail != "lock") || len(locks.held) != 0 {
				t.Fatalf("fail=%s err=%v called=%v held=%v", fail, err, called, locks.held)
			}
			want := []string{"container-a", "container-z"}
			if fail == "lock" {
				want = want[:1]
			}
			if !reflect.DeepEqual(locks.acquired, want) {
				t.Fatal(locks.acquired)
			}
		}
	}
}

func TestRenameCheckpointBlocksOtherLifecycleChanges(t *testing.T) {
	for _, operation := range []string{"start", "ssh", "vnc", "stop", "remove", "backup", "update"} {
		t.Run(operation, func(t *testing.T) {
			runtime := newRuntimeFake()
			original := domain.Instance{Name: "a", Group: "업무", VNCPort: 30000, DesiredState: domain.DesiredStopped}
			runtime.instances["a"] = original
			runtime.pending["a"] = "rename"
			runtime.statuses["a"] = domain.StatusStopped
			service := application.NewInstances(runtime, &lockFake{})
			var err error
			switch operation {
			case "start":
				err = service.Start("a")
			case "ssh":
				err = service.SSH("a")
			case "vnc":
				err = service.VNC("a")
			case "stop":
				err = service.Stop("a")
			case "remove":
				err = service.Remove("a", true)
			case "backup":
				_, err = service.Backup("a")
			case "update":
				err = service.Update()
			}
			if err == nil || runtime.instances["a"] != original || runtime.pending["a"] != "rename" || runtime.statuses["a"] != domain.StatusStopped {
				t.Fatalf("operation=%s err=%v state=%+v", operation, err, runtime)
			}
		})
	}
}
