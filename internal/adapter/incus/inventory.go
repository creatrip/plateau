package incus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/creatrip/plateau/internal/domain"
)

var restoreIdentity = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (manager Manager) Inspect(name domain.Name) (domain.InstanceStatus, error) {
	if _, err := domain.ParseName(name.String()); err != nil {
		return domain.InstanceStatus{}, err
	}
	records, err := manager.inventory(name)
	if err != nil {
		return domain.InstanceStatus{}, err
	}
	if len(records) == 0 {
		return domain.InstanceStatus{}, fmt.Errorf("%w: %s", domain.ErrNotFound, name)
	}
	return records[0], nil
}

func (manager Manager) List() ([]domain.InstanceStatus, error) {
	return manager.inventory("")
}

// Both lookup and listing decode the same authoritative data. No local cache
// can hide a container or authorize operations on an unrelated namesake.
func (manager Manager) inventory(name domain.Name) ([]domain.InstanceStatus, error) {
	arguments := []string{"sudo", "incus", "list"}
	if name != "" {
		arguments = append(arguments, "^"+name.String()+"$")
	}
	arguments = append(arguments, "--format=json")
	output, err := manager.transport.Output(arguments)
	if err != nil {
		return nil, fmt.Errorf("inspect Incus inventory: %w", err)
	}
	var records []struct {
		Name   string            `json:"name"`
		Type   string            `json:"type"`
		Status string            `json:"status"`
		Config map[string]string `json:"config"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(&records); err != nil {
		return nil, fmt.Errorf("%w: decode Incus inventory: %v", domain.ErrInvalidState, err)
	}
	if records == nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("%w: trailing Incus inventory data", domain.ErrInvalidState)
	}
	result := make([]domain.InstanceStatus, 0, len(records))
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if seen[record.Name] || (name != "" && record.Name != name.String()) {
			return nil, fmt.Errorf("%w: unexpected or duplicate container %q", domain.ErrInvalidState, record.Name)
		}
		seen[record.Name] = true
		if record.Type != "container" || record.Config["user.plateau.managed"] != "true" {
			if name != "" {
				return nil, fmt.Errorf("%w: %s", domain.ErrNotManaged, name)
			}
			continue
		}
		pending := record.Config["user.plateau.pending"]
		renameFrom := domain.Name(record.Config["user.plateau.rename-from"])
		renameTo := domain.Name(record.Config["user.plateau.rename-to"])
		renameHosts := ""
		if pending == "rename" {
			var hosts *string
			encodedHosts := record.Config["user.plateau.rename-hosts"]
			if len(encodedHosts) > 8<<20 || json.Unmarshal([]byte(encodedHosts), &hosts) != nil || hosts == nil || len(*hosts) > 1<<20 {
				return nil, fmt.Errorf("%w: invalid hostname checkpoint for %q", domain.ErrInvalidState, record.Name)
			}
			renameHosts = *hosts
			_, fromErr := domain.ParseName(renameFrom.String())
			_, toErr := domain.ParseName(renameTo.String())
			if fromErr != nil || toErr != nil || renameFrom == renameTo || (record.Name != renameFrom.String() && record.Name != renameTo.String()) || record.Config["user.plateau.name"] != renameFrom.String() || record.Config["boot.autostart"] != "false" || !strings.EqualFold(record.Status, "stopped") {
				return nil, fmt.Errorf("%w: invalid rename checkpoint for %q", domain.ErrInvalidState, record.Name)
			}
		}
		if record.Config["user.plateau.name"] != record.Name && pending != "rename" {
			// An imported archive is hidden until its validated ports and retry
			// metadata have been committed and Incus renames it to its real name.
			sourceID := record.Config["user.plateau.restore-id"]
			port, portErr := strconv.ParseUint(record.Config["user.plateau.vnc-port"], 10, 16)
			original := domain.Instance{Name: domain.Name(record.Config["user.plateau.name"]), Group: record.Config["user.plateau.group"], VNCPort: uint16(port), DesiredState: domain.DesiredState(record.Config["user.plateau.desired"])}
			if restoreIdentity.MatchString(sourceID) && record.Name == "plateau-import-"+sourceID[:24] && record.Config["user.plateau.pending"] == "restore" && record.Config["boot.autostart"] == "false" && strings.EqualFold(record.Status, "stopped") && portErr == nil && original.Validate() == nil {
				if name != "" {
					return nil, fmt.Errorf("%w: internal restore staging", domain.ErrNotManaged)
				}
				continue
			}
			return nil, fmt.Errorf("%w: container name metadata differs for %q", domain.ErrInvalidState, record.Name)
		}
		port, err := strconv.ParseUint(record.Config["user.plateau.vnc-port"], 10, 16)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid port for %q", domain.ErrInvalidState, record.Name)
		}
		instance := domain.Instance{Name: domain.Name(record.Name), Group: record.Config["user.plateau.group"], VNCPort: uint16(port)}
		// boot.autostart is the setting Incus actually uses at host startup.
		// The old user.plateau.desired field is retained only in legacy backups.
		switch record.Config["boot.autostart"] {
		case "true":
			instance.DesiredState = domain.DesiredRunning
		case "false":
			instance.DesiredState = domain.DesiredStopped
		default:
			return nil, fmt.Errorf("%w: missing restart policy for %q", domain.ErrInvalidState, record.Name)
		}
		if err := instance.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", domain.ErrInvalidState, err)
		}
		if pending != "" && pending != "create" && pending != "restore" && pending != "rename" {
			return nil, fmt.Errorf("%w: unknown pending operation %q", domain.ErrInvalidState, pending)
		}
		if pending == "restore" && !restoreIdentity.MatchString(record.Config["user.plateau.restore-id"]) {
			return nil, fmt.Errorf("%w: missing restore identity for %q", domain.ErrInvalidState, record.Name)
		}
		status := domain.RuntimeStatus(strings.ToLower(record.Status))
		switch status {
		case "running", "stopped", "frozen", "starting", "stopping", "freezing", "thawed":
		default:
			status = domain.StatusBroken
		}
		result = append(result, domain.InstanceStatus{Instance: instance, RuntimeStatus: status, PendingOperation: pending, RestoreID: record.Config["user.plateau.restore-id"], RenameFrom: renameFrom, RenameTo: renameTo, RenameHosts: renameHosts, RenamedFrom: domain.Name(record.Config["user.plateau.renamed-from"])})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
