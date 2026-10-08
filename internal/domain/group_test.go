package domain_test

import (
	"strings"
	"testing"

	"github.com/creatrip/plateau/internal/domain"
)

func TestGroupValidation(t *testing.T) {
	for _, group := range []string{"", "업무", "업무 자동화", "team/a", strings.Repeat("한", 64)} {
		if err := domain.ValidateGroup(group); err != nil {
			t.Errorf("valid group %q: %v", group, err)
		}
	}
	for _, group := range []string{" 업무", "업무 ", "a\nb", "a\tb", "\x1b[31m", "\xff", strings.Repeat("한", 65)} {
		if err := domain.ValidateGroup(group); err == nil {
			t.Errorf("accepted invalid group %q", group)
		}
	}
}
