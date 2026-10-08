package lima

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/creatrip/plateau/internal/adapter/process"
)

const (
	plateauHostName     = "plateau-host"
	plateauDataDiskName = "plateau-data"
	hostDiskGiB         = "32"
	dataDiskSize        = "1TiB"
)

// Only explicit probe exit codes authorize repair. A failed transport or
// executable is not evidence that a dependency/configuration is absent.
func missingPrerequisite(err error, codes ...int) bool {
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) {
		return false
	}
	for _, code := range codes {
		if exit.ExitCode() == code {
			return true
		}
	}
	return false
}

type hostCommandRunner interface {
	Run(command string, args []string, stdin io.Reader, stdout, stderr io.Writer) error
	Output(command string, args []string) ([]byte, error)
}

type systemHostRunner = process.Runner

type Host struct {
	stdout io.Writer
	stderr io.Writer
	runner hostCommandRunner
	wait   func(time.Duration)
}

func NewHost(ctx context.Context, stdout, stderr io.Writer) Host {
	return Host{stdout: stdout, stderr: stderr, runner: systemHostRunner{Context: ctx}, wait: func(delay time.Duration) {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}}
}

func (host Host) Ensure() error {
	diskOutput, err := host.runner.Output("limactl", []string{"disk", "list", "--json"})
	if err != nil {
		return fmt.Errorf("inspect Plateau data disk: %w", err)
	}
	var disks []struct {
		Name string `json:"name"`
	}
	trimmedDiskOutput := bytes.TrimSpace(diskOutput)
	if len(trimmedDiskOutput) > 0 {
		if trimmedDiskOutput[0] == '[' {
			if err := json.Unmarshal(trimmedDiskOutput, &disks); err != nil {
				return fmt.Errorf("decode Lima disk list: %w", err)
			}
		} else {
			decoder := json.NewDecoder(bytes.NewReader(trimmedDiskOutput))
			for {
				var disk struct {
					Name string `json:"name"`
				}
				if err := decoder.Decode(&disk); err == io.EOF {
					break
				} else if err != nil {
					return fmt.Errorf("decode Lima disk list: %w", err)
				}
				disks = append(disks, disk)
			}
		}
	}
	dataDiskExists := false
	for _, disk := range disks {
		if disk.Name == plateauDataDiskName {
			dataDiskExists = true
		}
	}
	if !dataDiskExists {
		if err := host.runner.Run("limactl", []string{"disk", "create", "--tty=false", plateauDataDiskName, "--size", dataDiskSize, "--format", "raw"}, nil, host.stdout, host.stderr); err != nil {
			return fmt.Errorf("create Plateau data disk: %w", err)
		}
	}

	status := ""
	created := false
	dataDiskAttached := false
	dataDiskFormat := false
	dataDiskFSType := ""
	for attempt := 0; ; attempt++ {
		output, err := host.runner.Output("limactl", []string{"list", "--format=json"})
		if err != nil {
			return fmt.Errorf("inspect Plateau host: %w", err)
		}
		type hostRecord struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Config struct {
				AdditionalDisks []struct {
					Name   string `json:"name"`
					Format bool   `json:"format"`
					FSType string `json:"fsType"`
				} `json:"additionalDisks"`
			} `json:"config"`
		}
		var records []hostRecord
		decoder := json.NewDecoder(bytes.NewReader(output))
		for {
			var record hostRecord
			if err := decoder.Decode(&record); err == io.EOF {
				break
			} else if err != nil {
				return fmt.Errorf("decode Lima host list: %w", err)
			}
			records = append(records, record)
		}
		status = ""
		dataDiskAttached = false
		dataDiskFormat = false
		dataDiskFSType = ""
		for _, record := range records {
			if record.Name == plateauHostName {
				if status != "" {
					return fmt.Errorf("multiple Lima hosts named %q", plateauHostName)
				}
				status = strings.ToLower(record.Status)
				for _, disk := range record.Config.AdditionalDisks {
					if disk.Name == plateauDataDiskName {
						dataDiskAttached = true
						dataDiskFormat = disk.Format
						dataDiskFSType = disk.FSType
					}
				}
			}
		}
		if status != "broken" || attempt == 39 {
			break
		}
		host.wait(250 * time.Millisecond)
	}
	if status == "" {
		if err := host.runner.Run("limactl", []string{
			"create", "--tty=false", "--name", plateauHostName,
			"--vm-type", "vz", "--arch", "aarch64",
			"--cpus", "4", "--memory", "8", "--disk", hostDiskGiB,
			"--containerd", "none", "--mount-none", "-",
		}, strings.NewReader(incusHostTemplate), host.stdout, host.stderr); err != nil {
			return fmt.Errorf("create Plateau host: %w", err)
		}
		status = "stopped"
		created = true
	}
	if status == "broken" {
		return fmt.Errorf("Plateau host is broken")
	}
	if dataDiskAttached && dataDiskFSType != "btrfs" {
		return fmt.Errorf("Plateau data disk is attached with unsupported filesystem %q", dataDiskFSType)
	}
	// Probe live health in one SSH connection. Never cache readiness across boots:
	// a stopped service or missing mount must still reach the repair path below.
	if status == "running" && dataDiskAttached && !dataDiskFormat {
		output, err := host.Output([]string{"sudo", "sh", "-eu", "-c", hostHealthCheck})
		if err != nil && !missingPrerequisite(err, 1, 3) {
			return fmt.Errorf("inspect host readiness: %w", err)
		}
		if err == nil && string(output) != "ready\n" {
			return fmt.Errorf("inspect host readiness: unexpected response %q", output)
		}
		if err == nil {
			label := fmt.Sprintf("gui/%d/io.lima-vm.autostart.%s", os.Getuid(), plateauHostName)
			if _, err := host.runner.Output("launchctl", []string{"print", label}); err != nil {
				// launchctl print returns 113 for an absent job on macOS.
				if !missingPrerequisite(err, 113) {
					return fmt.Errorf("inspect login recovery: %w", err)
				}
				if err := host.runner.Run("limactl", []string{"autostart", "enable", "--tty=false", plateauHostName}, nil, io.Discard, io.Discard); err != nil {
					return fmt.Errorf("enable login recovery: %w", err)
				}
			}
			return nil
		}
	}
	if status != "running" {
		if err := host.runner.Run("limactl", []string{"start", "--tty=false", plateauHostName}, nil, host.stdout, host.stderr); err != nil {
			return fmt.Errorf("start Plateau host: %w", err)
		}
	}
	for attempt := 0; ; attempt++ {
		if _, err := host.Output([]string{"true"}); err == nil {
			break
		} else if attempt == 39 {
			return fmt.Errorf("wait for Plateau host SSH: %w", err)
		}
		host.wait(250 * time.Millisecond)
	}
	installedIncus := false
	// Repair only after the read-only fast path fails. The individual checks
	// below identify what is missing without reinstalling healthy dependencies.
	if _, err := host.Output([]string{"test", "-x", "/usr/bin/incus", "-a", "-x", "/usr/bin/newuidmap", "-a", "-x", "/usr/bin/newgidmap", "-a", "-x", "/usr/sbin/dnsmasq", "-a", "-x", "/usr/sbin/mkfs.btrfs", "-a", "-x", "/usr/bin/nc", "-a", "-x", "/usr/bin/zstd"}); err != nil {
		if !missingPrerequisite(err, 1) {
			return fmt.Errorf("inspect host dependencies: %w", err)
		}
		if err := host.Run([]string{"sudo", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update"}, nil, host.stdout, host.stderr); err != nil {
			return fmt.Errorf("update package index for Incus: %w", err)
		}
		if err := host.Run([]string{"sudo", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "--no-install-recommends", "incus-base", "uidmap", "dnsmasq-base", "btrfs-progs", "netcat-openbsd", "zstd"}, nil, host.stdout, host.stderr); err != nil {
			return fmt.Errorf("install Incus: %w", err)
		}
		installedIncus = true
	}
	if _, err := host.Output([]string{"sudo", "sh", "-eu", "-c", incusServiceCheck}); installedIncus || err != nil {
		if err != nil && !missingPrerequisite(err, 1, 3) {
			return fmt.Errorf("inspect Incus service: %w", err)
		}
		if err := host.Run([]string{"sudo", "systemctl", "enable", "--now", "incus.service", "incus.socket"}, nil, io.Discard, io.Discard); err != nil {
			return fmt.Errorf("start Incus: %w", err)
		}
	}
	if _, err := host.Output([]string{"sudo", "sh", "-eu", "-c", incusConfigurationCheck}); err != nil {
		if !missingPrerequisite(err, 1) {
			return fmt.Errorf("inspect Incus configuration: %w", err)
		}
		// A failed config assertion can also mean existing, conflicting setup.
		// Initialize only after positively identifying an empty Incus install.
		for _, resource := range []string{"storage-pools", "networks", "profiles/default"} {
			probe := []string{"sudo", "incus", "query", "/1.0/" + resource + "?recursion=1"}
			if resource == "storage-pools" {
				// Incus 6.0.4의 query는 빈 저장소에 빈 출력을 반환합니다.
				// list의 명시적인 JSON 배열로 확인하고 읽기 실패는 그대로 거부합니다.
				probe = []string{"sudo", "incus", "storage", "list", "--format=json"}
			}
			output, err := host.Output(probe)
			if err != nil {
				return fmt.Errorf("inspect existing Incus %s: %w", resource, err)
			}
			if resource == "profiles/default" {
				var profile struct {
					Devices map[string]json.RawMessage `json:"devices"`
					Config  map[string]string          `json:"config"`
				}
				if err := json.Unmarshal(output, &profile); err != nil {
					return fmt.Errorf("decode Incus default profile: %w", err)
				}
				if profile.Devices == nil || len(profile.Devices) != 0 || len(profile.Config) != 0 {
					return fmt.Errorf("Incus default profile conflicts with Plateau configuration; refusing reinitialization")
				}
			} else {
				var records []struct {
					Managed bool `json:"managed"`
				}
				if err := json.Unmarshal(output, &records); err != nil {
					return fmt.Errorf("decode Incus %s: %w", resource, err)
				}
				if records == nil {
					return fmt.Errorf("missing Incus %s inventory", resource)
				}
				for _, record := range records {
					if resource == "storage-pools" || record.Managed {
						return fmt.Errorf("existing Incus %s conflicts with Plateau configuration; refusing reinitialization", resource)
					}
				}
			}
		}
		if err := host.Run([]string{"sudo", "incus", "admin", "init", "--preseed"}, strings.NewReader(incusPreseed), host.stdout, host.stderr); err != nil {
			return fmt.Errorf("initialize Incus: %w", err)
		}
	}
	if _, err := host.Output([]string{"grep", "-qw", "btrfs", "/proc/filesystems"}); err != nil {
		if !missingPrerequisite(err, 1) {
			return fmt.Errorf("inspect Btrfs support: %w", err)
		}
		if err := host.Run([]string{"sudo", "modprobe", "btrfs"}, nil, io.Discard, io.Discard); err != nil {
			return fmt.Errorf("load Btrfs filesystem support: %w", err)
		}
	}
	if !dataDiskAttached {
		if err := host.runner.Run("limactl", []string{"stop", plateauHostName}, nil, host.stdout, host.stderr); err != nil {
			return fmt.Errorf("stop Plateau host to attach data disk: %w", err)
		}
		if err := host.runner.Run("limactl", []string{"edit", "--tty=false", plateauHostName, "--set", `.additionalDisks += [{"name":"plateau-data","format":true,"fsType":"btrfs"}]`}, nil, host.stdout, host.stderr); err != nil {
			return fmt.Errorf("attach Plateau data disk: %w", err)
		}
		if err := host.runner.Run("limactl", []string{"start", "--tty=false", plateauHostName}, nil, host.stdout, host.stderr); err != nil {
			return fmt.Errorf("restart Plateau host with data disk: %w", err)
		}
		for attempt := 0; ; attempt++ {
			if _, err := host.Output([]string{"true"}); err == nil {
				break
			} else if attempt == 39 {
				return fmt.Errorf("wait for restarted Plateau host SSH: %w", err)
			}
			host.wait(250 * time.Millisecond)
		}
		dataDiskFormat = true
	}
	var storageOutput []byte
	for attempt := 0; ; attempt++ {
		storageOutput, err = host.Output([]string{"sudo", "incus", "storage", "list", "--format=json"})
		if err == nil {
			break
		}
		if attempt == 39 {
			return fmt.Errorf("inspect Incus storage pools: %w", err)
		}
		host.wait(250 * time.Millisecond)
	}
	var storagePools []struct {
		Name   string `json:"name"`
		Driver string `json:"driver"`
		Status string `json:"status"`
	}
	decoder := json.NewDecoder(bytes.NewReader(storageOutput))
	if err := decoder.Decode(&storagePools); err != nil {
		return fmt.Errorf("decode Incus storage pools: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("decode Incus storage pools: trailing data")
	}
	plateauPoolExists := false
	for _, pool := range storagePools {
		if pool.Name != "plateau" {
			continue
		}
		if pool.Driver != "btrfs" || strings.ToLower(pool.Status) != "created" {
			return fmt.Errorf("Incus storage pool %q is not a created Btrfs pool", pool.Name)
		}
		plateauPoolExists = true
	}
	if !plateauPoolExists {
		// Formatting is permitted only for a new, empty disk. Never infer that an
		// existing disk is disposable merely because its pool is unavailable.
		for attempt := 0; ; attempt++ {
			if _, err := host.Output([]string{"test", "-b", "/dev/disk/by-label/lima-plateau-data"}); err == nil {
				break
			} else if attempt == 119 {
				return fmt.Errorf("wait for Plateau data disk: %w", err)
			}
			host.wait(250 * time.Millisecond)
		}
		deviceOutput, err := host.Output([]string{"readlink", "-f", "/dev/disk/by-label/lima-plateau-data"})
		if err != nil {
			return fmt.Errorf("resolve Plateau data disk device: %w", err)
		}
		dataDevice := strings.TrimSpace(string(deviceOutput))
		if !strings.HasPrefix(dataDevice, "/dev/") || strings.ContainsAny(dataDevice, " \t\r\n") {
			return fmt.Errorf("validate Plateau data disk device %q", dataDevice)
		}
		if _, err := host.Output([]string{"test", "-b", dataDevice}); err != nil {
			return fmt.Errorf("validate Plateau data disk block device %q: %w", dataDevice, err)
		}
		prepareStorageScript := `mkdir -p /mnt/lima-plateau-data
if ! mountpoint -q /mnt/lima-plateau-data; then
  mount /dev/disk/by-label/lima-plateau-data /mnt/lima-plateau-data
fi
if find /mnt/lima-plateau-data -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
  echo "Plateau data disk is not empty; refusing to format it" >&2
  exit 1
fi
if btrfs subvolume list /mnt/lima-plateau-data | grep -q .; then
  echo "Plateau data disk contains Btrfs subvolumes; refusing to format it" >&2
  exit 1
fi
umount /mnt/lima-plateau-data`
		if err := host.Run([]string{"sudo", "sh", "-eu", "-c", prepareStorageScript}, nil, host.stdout, host.stderr); err != nil {
			return fmt.Errorf("validate empty Plateau data disk: %w", err)
		}
		if err := host.Run([]string{"sudo", "incus", "storage", "create", "plateau", "btrfs", "source=" + dataDevice, "source.wipe=true", "btrfs.mount_options=compress=zstd,discard=async,user_subvol_rm_allowed"}, nil, host.stdout, host.stderr); err != nil {
			return fmt.Errorf("create Incus Btrfs storage pool: %w", err)
		}
		plateauPoolExists = true
	}
	if plateauPoolExists && dataDiskFormat {
		if err := host.runner.Run("limactl", []string{"stop", plateauHostName}, nil, host.stdout, host.stderr); err != nil {
			return fmt.Errorf("stop Plateau host to hand data disk ownership to Incus: %w", err)
		}
		if err := host.runner.Run("limactl", []string{"edit", "--tty=false", plateauHostName, "--set", `(.additionalDisks[] | select(.name == "plateau-data") | .format) = false`}, nil, host.stdout, host.stderr); err != nil {
			return fmt.Errorf("disable Lima data disk formatting: %w", err)
		}
		if err := host.runner.Run("limactl", []string{"start", "--tty=false", plateauHostName}, nil, host.stdout, host.stderr); err != nil {
			return fmt.Errorf("restart Plateau host after storage setup: %w", err)
		}
		for attempt := 0; ; attempt++ {
			if _, err := host.Output([]string{"sudo", "incus", "storage", "show", "plateau"}); err == nil {
				break
			} else if attempt == 119 {
				return fmt.Errorf("wait for Incus Btrfs storage after restart: %w", err)
			}
			host.wait(250 * time.Millisecond)
		}
	}
	compressionScript := `mount -o remount,compress=zstd,discard=async /var/lib/incus/storage-pools/plateau
btrfs property set /var/lib/incus/storage-pools/plateau compression zstd
btrfs subvolume list -o /var/lib/incus/storage-pools/plateau | sed -n 's/.* path //p' | while IFS= read -r subvolume; do
  path="/var/lib/incus/storage-pools/plateau/$subvolume"
  if [ "$(btrfs property get "$path" ro)" = "ro=false" ]; then
    btrfs property set "$path" compression zstd
  fi
done`
	if err := host.Run([]string{"sudo", "sh", "-eu", "-c", compressionScript}, nil, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("enable Plateau Btrfs compression: %w", err)
	}
	autostartLabel := fmt.Sprintf("gui/%d/io.lima-vm.autostart.%s", os.Getuid(), plateauHostName)
	registerAutostart := created
	if !created {
		_, err := host.runner.Output("launchctl", []string{"print", autostartLabel})
		if err != nil && !missingPrerequisite(err, 113) {
			return fmt.Errorf("inspect login recovery: %w", err)
		}
		registerAutostart = err != nil
	}
	if registerAutostart {
		if err := host.runner.Run("limactl", []string{"autostart", "enable", "--tty=false", plateauHostName}, nil, io.Discard, io.Discard); err != nil {
			return fmt.Errorf("enable Plateau host recovery: %w", err)
		}
	}
	return nil
}

func (host Host) Update() error {
	if err := host.Ensure(); err != nil {
		return err
	}
	if err := host.Run([]string{"sudo", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update"}, nil, host.stdout, host.stderr); err != nil {
		return fmt.Errorf("update Plateau host package index: %w", err)
	}
	if err := host.Run([]string{"sudo", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "upgrade", "-y", "--with-new-pkgs"}, nil, host.stdout, host.stderr); err != nil {
		return fmt.Errorf("update Plateau host packages: %w", err)
	}
	return nil
}
