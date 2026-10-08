package cli_test

import (
	"bytes"
	"github.com/creatrip/plateau/internal/adapter/cli"
	"github.com/creatrip/plateau/internal/application"
	"reflect"
	"testing"
)

func TestRenameCommandValidatesBothNamesBeforeBootstrap(t *testing.T) {
	for _, args := range [][]string{{"rename", "old", "new"}, {"rename", "old", "../bad"}, {"rename", "../bad", "new"}, {"rename", "a", "a"}, {"rename", "a"}, {"rename", "a", "b", "c"}} {
		valid := reflect.DeepEqual(args, []string{"rename", "old", "new"})
		var output bytes.Buffer
		bootstrapped := false
		service := &instanceServiceStub{}
		app := cli.New("test", application.NewBootstrap(lockStub{}, hostProvisionerStub{ensure: func() error { bootstrapped = true; return nil }}), service, &output, &output)
		code := app.Run(args)
		if (code == 0) != valid || bootstrapped != valid {
			t.Fatalf("args=%v code=%d bootstrap=%v output=%s", args, code, bootstrapped, &output)
		}
		if valid && !reflect.DeepEqual(service.calls, []string{"rename old new"}) {
			t.Fatal(service.calls)
		}
		if !valid && len(service.calls) != 0 {
			t.Fatal(service.calls)
		}
	}
}
