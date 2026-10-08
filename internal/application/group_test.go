package application_test

import (
	"errors"
	"github.com/creatrip/plateau/internal/application"
	"testing"
)

func TestCreatePreservesGroupAcrossRetryAndStart(t *testing.T) {
	for _, retryWithStart := range []bool{false, true} {
		runtime := newRuntimeFake()
		runtime.faults["prepare"] = errors.New("interrupted")
		service := application.NewInstances(runtime, &lockFake{})
		group := "업무"
		if err := service.Create("a", &group); err == nil {
			t.Fatal("expected interruption")
		}
		if runtime.instances["a"].Group != group {
			t.Fatal("group missing from checkpoint")
		}
		other := "운영"
		if err := service.Create("a", &other); err == nil {
			t.Fatal("conflicting retry accepted")
		}
		delete(runtime.faults, "prepare")
		var err error
		if retryWithStart {
			err = service.Start("a")
		} else {
			err = service.Create("a", nil)
		}
		if err != nil || runtime.instances["a"].Group != group {
			t.Fatalf("group lost: %v %+v", err, runtime.instances)
		}
	}
}
