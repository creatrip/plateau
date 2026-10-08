package cli_test

import (
	"bytes"
	"github.com/creatrip/plateau/internal/adapter/cli"
	"github.com/creatrip/plateau/internal/application"
	"github.com/creatrip/plateau/internal/domain"
	"reflect"
	"strings"
	"testing"
)

func TestGroupCommandsAndValidation(t *testing.T) {
	for _, test := range []struct {
		args  []string
		group string
		call  string
		valid bool
	}{
		{[]string{"create", "a", "--group", "업무 자동화"}, "업무 자동화", "create a", true},
		{[]string{"regroup", "a", "운영"}, "운영", "regroup a", true},
		{[]string{"regroup", "a", "--clear"}, "", "regroup a", true},
		{[]string{"regroup", "a", "운영", "--clear"}, "", "", false},
		{[]string{"regroup", "a"}, "", "", false},
		{[]string{"regroup", "a", ""}, "", "", false},
		{[]string{"regroup", "../bad", "운영"}, "", "", false},
		{[]string{"create", "a", "--group", "a\nb"}, "", "", false},
		{[]string{"regroup", "a", " 업무"}, "", "", false},
		{[]string{"ls", "--group", "\x1b"}, "", "", false},
	} {
		t.Run(strings.Join(test.args, "/"), func(t *testing.T) {
			var output bytes.Buffer
			bootstrapped := false
			service := &instanceServiceStub{}
			app := cli.New("test", application.NewBootstrap(lockStub{}, hostProvisionerStub{ensure: func() error { bootstrapped = true; return nil }}), service, &output, &output)
			code := app.Run(test.args)
			if (code == 0) != test.valid || bootstrapped != test.valid {
				t.Fatalf("code=%d bootstrap=%v output=%s", code, bootstrapped, &output)
			}
			if test.valid {
				if !reflect.DeepEqual(service.calls, []string{test.call}) || service.group == nil || *service.group != test.group {
					t.Fatalf("calls=%v group=%v", service.calls, service.group)
				}
			} else if len(service.calls) != 0 {
				t.Fatalf("invalid request reached service: %v", service.calls)
			}
		})
	}
}

func TestListGroupsContainersWithoutOptions(t *testing.T) {
	service := &instanceServiceStub{list: []domain.InstanceStatus{
		{Instance: domain.Instance{Name: "d", Group: "운영"}},
		{Instance: domain.Instance{Name: "b", Group: "개발"}},
		{Instance: domain.Instance{Name: "a"}},
		{Instance: domain.Instance{Name: "c", Group: "운영"}},
		{Instance: domain.Instance{Name: "e", Group: "개발"}},
	}}
	var output bytes.Buffer
	app := cli.New("test", application.NewBootstrap(lockStub{}, hostProvisionerStub{}), service, &output, &output)
	if code := app.Run([]string{"ls"}); code != 0 {
		t.Fatal(output.String())
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n")[1:] {
		names = append(names, strings.Fields(line)[0])
	}
	if !reflect.DeepEqual(names, []string{"b", "e", "c", "d", "a"}) {
		t.Fatalf("names=%v output=%s", names, &output)
	}
}

func TestListPrintsGroupTagsAsEntered(t *testing.T) {
	for _, test := range []struct {
		group string
		label string
	}{
		{"team-alpha", "team-alpha"},
		{"업무 자동화", "업무 자동화"},
		{`team "a"\dev`, `team "a"\dev`},
		{"", "-"},
	} {
		t.Run(test.label, func(t *testing.T) {
			service := &instanceServiceStub{list: []domain.InstanceStatus{
				{Instance: domain.Instance{Name: "work-a", Group: test.group}, RuntimeStatus: domain.StatusRunning},
			}}
			var output bytes.Buffer
			app := cli.New("test", application.NewBootstrap(lockStub{}, hostProvisionerStub{}), service, &output, &output)
			if code := app.Run([]string{"ls"}); code != 0 {
				t.Fatal(output.String())
			}
			want := "NAME    STATUS   DISK  GROUP\nwork-a  running  -     " + test.label + "\n"
			if output.String() != want {
				t.Fatalf("output = %q, want %q", output.String(), want)
			}
		})
	}
}

func TestRemovedGroupCommandAndListOptionsDoNotBootstrap(t *testing.T) {
	for _, args := range [][]string{{"group", "a", "운영"}, {"set-group", "a", "운영"}, {"ls", "--group", "운영"}, {"ls", "--group", ""}, {"ls", "--grouped"}} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			var output bytes.Buffer
			bootstrapped := false
			service := &instanceServiceStub{}
			app := cli.New("test", application.NewBootstrap(lockStub{}, hostProvisionerStub{ensure: func() error { bootstrapped = true; return nil }}), service, &output, &output)
			if code := app.Run(args); code == 0 || bootstrapped || len(service.calls) != 0 {
				t.Fatalf("code=%d bootstrap=%v calls=%v output=%s", code, bootstrapped, service.calls, &output)
			}
		})
	}
}
