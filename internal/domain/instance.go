package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Missing, unowned and unreadable containers require different recovery paths.
// In particular, an inspection failure must never authorize replacement.
var (
	ErrNotFound     = errors.New("container not found")
	ErrNotManaged   = errors.New("container is not managed by Plateau")
	ErrInvalidState = errors.New("invalid container state")
)

var namePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

type Name string

func ParseName(value string) (Name, error) {
	if !namePattern.MatchString(value) {
		return "", fmt.Errorf("invalid instance name %q: use 1-40 lowercase letters, digits, or hyphens and start and end with a letter or digit", value)
	}
	return Name(value), nil
}

func (name Name) String() string {
	return string(name)
}

func ValidateGroup(value string) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 64 || strings.TrimSpace(value) != value || strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029'
	}) {
		return fmt.Errorf("invalid group %q: use up to 64 characters without surrounding whitespace or control characters", value)
	}
	return nil
}

type DesiredState string

// DesiredState is restart intent, not observed runtime state. A temporarily
// stopped backup must retain DesiredRunning so a later host boot restores it.
const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
)

type RuntimeStatus string

const (
	StatusRunning RuntimeStatus = "running"
	StatusStopped RuntimeStatus = "stopped"
	StatusBroken  RuntimeStatus = "broken"
)

type Instance struct {
	Name         Name         `json:"name"`
	Group        string       `json:"group,omitempty"`
	VNCPort      uint16       `json:"vncPort"`
	DesiredState DesiredState `json:"desiredState"`
}

func (instance Instance) Validate() error {
	if err := ValidateGroup(instance.Group); err != nil {
		return err
	}
	if _, err := ParseName(instance.Name.String()); err != nil {
		return err
	}
	if instance.VNCPort < 30000 || instance.VNCPort > 39999 {
		return fmt.Errorf("VNC port %d is outside 30000-39999", instance.VNCPort)
	}
	if instance.DesiredState != DesiredRunning && instance.DesiredState != DesiredStopped {
		return fmt.Errorf("invalid desired state %q", instance.DesiredState)
	}
	return nil
}

type InstanceStatus struct {
	Instance
	PendingOperation string
	RenameFrom       Name
	RenameTo         Name
	RenameHosts      string
	RenamedFrom      Name
	RestoreID        string
	RuntimeStatus    RuntimeStatus
	DiskUsageBytes   uint64
	DiskUsageKnown   bool
}
