package incus

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/creatrip/plateau/internal/domain"
)

func (manager Manager) Status(name domain.Name) (domain.RuntimeStatus, error) {
	record, err := manager.Inspect(name)
	return record.RuntimeStatus, err
}

func (manager Manager) DiskUsage(names []domain.Name) (map[domain.Name]uint64, error) {
	usage := make(map[domain.Name]uint64, len(names))
	if len(names) == 0 {
		return usage, nil
	}
	arguments := []string{"sudo", "btrfs", "filesystem", "du", "--raw", "--summarize", "--"}
	pathNames := make(map[string]domain.Name, len(names))
	for _, name := range names {
		incusName, err := incusInstanceName(name)
		if err != nil {
			return nil, err
		}
		path := "/var/lib/incus/storage-pools/" + managedStoragePool + "/containers/" + incusName
		arguments = append(arguments, path)
		pathNames[path] = name
	}
	output, err := manager.transport.Output(arguments)
	if err != nil {
		return nil, fmt.Errorf("inspect Btrfs disk usage: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	header := strings.Fields(lines[0])
	if len(header) != 5 || header[0] != "Total" || header[1] != "Exclusive" || header[2] != "Set" || header[3] != "shared" || header[4] != "Filename" {
		return nil, fmt.Errorf("decode Btrfs disk usage: unexpected header")
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return nil, fmt.Errorf("decode Btrfs disk usage: malformed row")
		}
		name, exists := pathNames[fields[3]]
		if !exists {
			return nil, fmt.Errorf("decode Btrfs disk usage: unexpected path %q", fields[3])
		}
		exclusiveBytes, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("decode Btrfs disk usage for %q: %w", name, err)
		}
		if _, exists := usage[name]; exists {
			return nil, fmt.Errorf("decode Btrfs disk usage: duplicate container %q", name)
		}
		usage[name] = exclusiveBytes
	}
	if len(usage) != len(pathNames) {
		return nil, fmt.Errorf("decode Btrfs disk usage: got %d of %d containers", len(usage), len(pathNames))
	}
	return usage, nil
}
