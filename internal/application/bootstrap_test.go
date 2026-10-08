package application_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/creatrip/plateau/internal/application"
)

type provisionerFake struct {
	name    string
	locks   *lockFake
	calls   *[]string
	failure error
}

func (host provisionerFake) Ensure() error {
	if !host.locks.held["bootstrap"] {
		return errors.New("unlocked provisioning")
	}
	*host.calls = append(*host.calls, "ensure "+host.name)
	return host.failure
}

func (host provisionerFake) Update() error {
	if !host.locks.held["bootstrap"] {
		return errors.New("unlocked update")
	}
	*host.calls = append(*host.calls, "update "+host.name)
	return host.failure
}

func TestBootstrapSerializesOrderedHostsAndReleasesOnFailure(t *testing.T) {
	for _, operation := range []string{"ensure", "update"} {
		for _, phase := range []string{"success", "lock", "installer", "host"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				locks := &lockFake{held: map[string]bool{}}
				var calls []string
				failure := errors.New("injected provisioning failure")
				installer := provisionerFake{name: "installer", locks: locks, calls: &calls}
				host := provisionerFake{name: "host", locks: locks, calls: &calls}
				var want []string
				switch phase {
				case "lock":
					locks.held["bootstrap"] = true
				case "installer":
					installer.failure = failure
					want = []string{operation + " installer"}
				case "host":
					host.failure = failure
					fallthrough
				case "success":
					want = []string{operation + " installer", operation + " host"}
				}
				bootstrap := application.NewBootstrap(locks, installer, host)
				var err error
				if operation == "ensure" {
					err = bootstrap.Run()
				} else {
					err = bootstrap.Update()
				}
				if (err == nil) != (phase == "success") {
					t.Fatalf("unexpected result: %v", err)
				}
				if phase == "installer" || phase == "host" {
					if !errors.Is(err, failure) {
						t.Fatalf("lost failure: %v", err)
					}
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("calls=%v want=%v", calls, want)
				}
				if locks.held["bootstrap"] != (phase == "lock") {
					t.Fatal("lock ownership was lost or leaked")
				}
			})
		}
	}
}
