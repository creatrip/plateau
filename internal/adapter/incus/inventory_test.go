package incus

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/creatrip/plateau/internal/domain"
)

func TestInventoryComesFromIncusWithoutLocalRecords(t *testing.T) {
	manager, transport := newManagerTest(t)
	transport.outputs["sudo\x00incus\x00list\x00--format=json"] = []byte(`[
		{"name":"b","type":"container","status":"Stopped","config":{"user.plateau.managed":"true","user.plateau.name":"b","user.plateau.vnc-port":"30001","boot.autostart":"false","user.plateau.desired":"running"}},
		{"name":"a","type":"container","status":"Running","config":{"user.plateau.managed":"true","user.plateau.name":"a","user.plateau.vnc-port":"30000","boot.autostart":"true"}},
		{"name":"unrelated","type":"container","status":"Running","config":{}}
	]`)
	records, err := manager.List()
	if err != nil || len(records) != 2 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	if records[0].Name != "a" || records[1].DesiredState != domain.DesiredStopped {
		t.Fatalf("expected sorted inventory and actual Incus restart policy: %+v", records)
	}
}

func TestInspectDistinguishesMissingUnownedAndInvalid(t *testing.T) {
	for _, test := range []struct {
		name, json string
		want       error
	}{
		{"missing", `[]`, domain.ErrNotFound},
		{"unowned", `[{"name":"a","type":"container","config":{}}]`, domain.ErrNotManaged},
		{"invalid", `[{"name":"a","type":"container","config":{"user.plateau.managed":"true","user.plateau.name":"a","user.plateau.vnc-port":"bad"}}]`, domain.ErrInvalidState},
		{"trailing", `[] []`, domain.ErrInvalidState},
		{"null", `null`, domain.ErrInvalidState},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, transport := newManagerTest(t)
			transport.outputs[strings.Join([]string{"sudo", "incus", "list", "^a$", "--format=json"}, "\x00")] = []byte(test.json)
			_, err := manager.Inspect("a")
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
		})
	}
}

func TestPortAllocationIncludesUnmanagedVNCAndSSHProxies(t *testing.T) {
	for _, offset := range []int{0, 10000} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			manager, transport := newManagerTest(t)
			var records []string
			for port := 30000; port <= 39999; port++ {
				records = append(records, fmt.Sprintf(`{"config":{},"expanded_devices":{"proxy":{"type":"proxy","listen":"tcp:0.0.0.0:%d"}}}`, port+offset))
			}
			transport.outputs["sudo\x00incus\x00list\x00--format=json"] = []byte("[" + strings.Join(records, ",") + "]")
			if port, err := manager.AllocateVNCPort(); err == nil {
				t.Fatalf("allocated colliding port %d", port)
			}
		})
	}
}

func TestInventoryNeverHidesUnverifiedRestoreStaging(t *testing.T) {
	sourceID := strings.Repeat("a", 64)
	for _, field := range []string{"user.plateau.restore-id", "user.plateau.pending", "boot.autostart", "user.plateau.name", "user.plateau.desired", "user.plateau.vnc-port", "status"} {
		t.Run(field, func(t *testing.T) {
			manager, transport := newManagerTest(t)
			config := map[string]string{
				"user.plateau.managed": "true", "user.plateau.name": "original",
				"user.plateau.restore-id": sourceID, "user.plateau.pending": "restore", "boot.autostart": "false",
				"user.plateau.vnc-port": "30000", "user.plateau.desired": "running",
			}
			status := "Stopped"
			config[field] = "invalid"
			if field == "status" {
				status = "Running"
				delete(config, field)
			}
			if field == "user.plateau.name" {
				config[field] = "../invalid"
			}
			records, err := json.Marshal([]any{map[string]any{"name": "plateau-import-" + sourceID[:24], "type": "container", "status": status, "config": config}})
			if err != nil {
				t.Fatal(err)
			}
			transport.outputs["sudo\x00incus\x00list\x00--format=json"] = records
			if _, err := manager.List(); !errors.Is(err, domain.ErrInvalidState) {
				t.Fatalf("unverified staging was hidden: %v", err)
			}
		})
	}
}
