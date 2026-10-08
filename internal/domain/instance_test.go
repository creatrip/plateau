package domain_test

import (
	"strings"
	"testing"

	"github.com/creatrip/plateau/internal/domain"
)

func TestParseNameAcceptsPlateauInstanceNames(t *testing.T) {
	for _, value := range []string{"a", "pg-a", "worker-12", strings.Repeat("a", 40)} {
		name, err := domain.ParseName(value)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		if name.String() != value {
			t.Fatalf("name = %q, want %q", name, value)
		}
	}
}

func TestParseNameRejectsUnsafeOrAmbiguousNames(t *testing.T) {
	for _, value := range []string{"", "PG-A", "pg_a", "-pg", "pg-", "pg.a", "pg/a", strings.Repeat("a", 41)} {
		if _, err := domain.ParseName(value); err == nil {
			t.Fatalf("parse %q succeeded, want error", value)
		}
	}
}

func TestInstanceValidationAcceptsPersistentState(t *testing.T) {
	instance := domain.Instance{Name: "pg-a", VNCPort: 32001, DesiredState: domain.DesiredRunning}

	if err := instance.Validate(); err != nil {
		t.Fatalf("validate instance: %v", err)
	}
}

func TestInstanceValidationRejectsInvalidState(t *testing.T) {
	for _, instance := range []domain.Instance{
		{Name: "PG-A", VNCPort: 32001, DesiredState: domain.DesiredRunning},
		{Name: "pg-a", VNCPort: 0, DesiredState: domain.DesiredRunning},
		{Name: "pg-a", VNCPort: 29999, DesiredState: domain.DesiredRunning},
		{Name: "pg-a", VNCPort: 40000, DesiredState: domain.DesiredRunning},
		{Name: "pg-a", VNCPort: 32001, DesiredState: "paused"},
	} {
		if err := instance.Validate(); err == nil {
			t.Fatalf("validate %+v succeeded, want error", instance)
		}
	}
}
