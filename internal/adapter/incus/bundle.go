package incus

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/creatrip/plateau/internal/domain"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	"go.yaml.in/yaml/v3"
)

const (
	bundleMagic         = "PLATEAU-INCUS-BUNDLE-V2\n"
	legacyBundleMagic   = "PLATEAU-INCUS-BUNDLE-V1\n"
	bundleFormatVersion = 2
	maximumManifestSize = 64 << 10
)

type bundleManifest struct {
	// Source identity binds a resumable import to both manifest and payload.
	// It is derived from the original manifest and digest in both bundle versions.
	sourceID      string
	checksum      []byte
	FormatVersion int             `json:"formatVersion"`
	Instance      domain.Instance `json:"instance"`
}

func (manager Manager) Backup(instance domain.Instance, destination string) error {
	if err := instance.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(destination) {
		return fmt.Errorf("backup destination must be absolute")
	}
	incusName, err := incusInstanceName(instance.Name)
	if err != nil {
		return err
	}
	if err := manager.runCommand([]string{
		"sudo", "incus", "config", "set", incusName,
		"user.plateau.managed=true",
		"user.plateau.name=" + instance.Name.String(),
		"user.plateau.group=" + instance.Group,
		"user.plateau.vnc-port=" + strconv.FormatUint(uint64(instance.VNCPort), 10),
		"user.plateau.desired=" + string(instance.DesiredState),
	}, nil, io.Discard, incusName, instance.Name.String()); err != nil {
		return fmt.Errorf("store Plateau backup metadata: %w", err)
	}
	temporaryDirectory, err := os.MkdirTemp(filepath.Dir(destination), ".plateau-backup-*")
	if err != nil {
		return fmt.Errorf("create backup workspace: %w", err)
	}
	defer os.RemoveAll(temporaryDirectory)
	hash := sha256.New()
	manifest, err := json.Marshal(bundleManifest{FormatVersion: bundleFormatVersion, Instance: instance})
	if err != nil {
		return fmt.Errorf("encode backup manifest: %w", err)
	}
	bundlePath := filepath.Join(temporaryDirectory, "bundle")
	bundle, err := os.OpenFile(bundlePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create backup bundle: %w", err)
	}
	// Reserve the fixed-size digest, stream the payload once, then fill it in.
	// Both supported formats use a fixed digest slot, avoiding another hash pass.
	checksumOffset := int64(len(bundleMagic) + 4 + len(manifest))
	var writeErr error
	if _, err := io.WriteString(bundle, bundleMagic); err != nil {
		writeErr = err
	} else if err := binary.Write(bundle, binary.BigEndian, uint32(len(manifest))); err != nil {
		writeErr = err
	} else if _, err := bundle.Write(manifest); err != nil {
		writeErr = err
	} else if _, err := io.WriteString(bundle, strings.Repeat("0", sha256.Size*2)+"\n"); err != nil {
		writeErr = err
	}
	if writeErr == nil {
		exportStarted := time.Now()
		fmt.Fprintln(manager.stderr, "컨테이너를 압축하고 Mac에 저장하는 중...")
		// Incus prints progress to stdout. FD 3 carries only the archive;
		// redirect progress to stderr and pass the container name as data.
		writeErr = manager.transport.Run([]string{"sudo", "sh", "-c",
			`exec incus export "$1" /dev/fd/3 --instance-only --compression="zstd -1" 3>&1 1>&2`,
			"sh", incusName,
		}, nil, io.MultiWriter(bundle, hash), manager.stderr)
		if writeErr != nil {
			writeErr = fmt.Errorf("export Incus container %q: %w", instance.Name, writeErr)
		} else {
			fmt.Fprintf(manager.stderr, "압축·전송 완료 (%.1f초)\n", time.Since(exportStarted).Seconds())
		}
	}
	if writeErr == nil {
		fmt.Fprintln(manager.stderr, "백업 파일을 마무리하는 중...")
		_, writeErr = bundle.WriteAt([]byte(hex.EncodeToString(hash.Sum(nil))), checksumOffset)
		if writeErr == nil {
			writeErr = bundle.Sync()
		}
	}
	if closeErr := bundle.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return fmt.Errorf("write backup bundle: %w", writeErr)
	}
	if err := os.Link(bundlePath, destination); err != nil {
		return fmt.Errorf("save backup without overwriting existing data: %w", err)
	}
	fmt.Fprintln(manager.stderr, "백업 파일 저장 완료. 컨테이너 상태를 복구하는 중...")
	return nil
}

func (manager Manager) ReadBackupManifest(bundlePath string) (domain.Instance, error) {
	// The manifest only selects a lock and checks name conflicts. RestoreBackup
	// verifies every payload byte before any import or container mutation.
	bundle, err := os.Open(bundlePath)
	if err != nil {
		return domain.Instance{}, fmt.Errorf("open Plateau backup: %w", err)
	}
	defer bundle.Close()
	manifest, err := readBundleManifest(bundle)
	if err != nil {
		return domain.Instance{}, err
	}
	return manifest.Instance, nil
}

func (manager Manager) RestoreBackup(bundlePath string, instance domain.Instance) error {
	if err := instance.Validate(); err != nil {
		return err
	}
	temporaryDirectory, err := os.MkdirTemp("", "plateau-restore-*")
	if err != nil {
		return fmt.Errorf("create restore workspace: %w", err)
	}
	defer os.RemoveAll(temporaryDirectory)
	payloadPath := filepath.Join(temporaryDirectory, "incus.tar.zst")
	preparationStarted := time.Now()
	fmt.Fprintln(manager.stderr, "백업을 검증하고 복원 파일을 준비하는 중...")
	manifest, err := readPlateauBundle(bundlePath, payloadPath)
	if err != nil {
		return err
	}
	if manifest.Instance.Name != instance.Name || manifest.Instance.DesiredState != instance.DesiredState || manifest.Instance.Group != instance.Group {
		return fmt.Errorf("backup manifest changed during restore")
	}
	fmt.Fprintf(manager.stderr, "백업 검증·복원 준비 완료 (%.1f초)\n", time.Since(preparationStarted).Seconds())
	incusName, err := incusInstanceName(instance.Name)
	if err != nil {
		return err
	}
	final, err := manager.Inspect(instance.Name)
	if err == nil {
		if final.PendingOperation != "restore" || final.RestoreID != manifest.sourceID || final.VNCPort != instance.VNCPort || final.Group != instance.Group {
			return fmt.Errorf("restore destination does not match this incomplete backup")
		}
		return nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	staging := "plateau-import-" + manifest.sourceID[:24]
	query := []string{"sudo", "incus", "list", "^" + staging + "$", "--format=json"}
	output, err := manager.transport.Output(query)
	if err != nil {
		return fmt.Errorf("inspect staged restore: %w", err)
	}
	var records []struct {
		Name    string                       `json:"name"`
		Type    string                       `json:"type"`
		Status  string                       `json:"status"`
		Config  map[string]string            `json:"config"`
		Devices map[string]map[string]string `json:"devices"`
	}
	if err := json.Unmarshal(output, &records); err != nil || records == nil || len(records) > 1 {
		return fmt.Errorf("invalid staged restore inventory")
	}
	if len(records) == 0 {
		payload, err := os.Open(payloadPath)
		if err != nil {
			return fmt.Errorf("open prepared restore payload: %w", err)
		}
		defer payload.Close()
		importStarted := time.Now()
		fmt.Fprintln(manager.stderr, "복원 파일을 전송하고 컨테이너를 복원하는 중...")
		if err := manager.runCommand([]string{"sudo", "incus", "import", "-", staging}, payload, io.Discard, staging, instance.Name.String()); err != nil {
			return fmt.Errorf("import container (retry the same backup to resume): %w", err)
		}
		fmt.Fprintf(manager.stderr, "컨테이너 데이터 복원 완료 (%.1f초)\n", time.Since(importStarted).Seconds())
		output, err = manager.transport.Output(query)
		if err != nil {
			return fmt.Errorf("inspect imported checkpoint (retry restore): %w", err)
		}
		if err := json.Unmarshal(output, &records); err != nil || len(records) != 1 {
			return fmt.Errorf("invalid imported checkpoint; retry restore")
		}
	}
	record := records[0]
	if record.Name != staging || record.Type != "container" || record.Config["user.plateau.managed"] != "true" || record.Config["user.plateau.name"] != instance.Name.String() || record.Config["user.plateau.desired"] != string(instance.DesiredState) || record.Config["user.plateau.group"] != instance.Group || record.Config["user.plateau.pending"] != "restore" || record.Config["user.plateau.restore-id"] != manifest.sourceID || record.Config["boot.autostart"] != "false" {
		return fmt.Errorf("staged container has conflicting ownership or state; refusing to modify it")
	}
	if !strings.EqualFold(record.Status, "stopped") {
		return fmt.Errorf("staged restore is not safely stopped: %s", record.Status)
	}
	// Neither import, metadata updates nor rename is a cross-command transaction.
	// A content-addressed, stopped staging instance survives interruption. Retrying
	// the same archive resumes it; nothing is deleted on a failed observation.
	for _, binding := range []struct {
		device         string
		offset, target int
	}{
		{"plateau-vnc", 0, 6080}, {"plateau-ssh", 10000, 22},
	} {
		device, exists := record.Devices[binding.device]
		if !exists && binding.device == "plateau-ssh" {
			continue
		}
		if !exists || device["type"] != "proxy" || device["connect"] != fmt.Sprintf("tcp:127.0.0.1:%d", binding.target) {
			return fmt.Errorf("staged restore has invalid %s", binding.device)
		}
		listen := fmt.Sprintf("listen=tcp:127.0.0.1:%d", int(instance.VNCPort)+binding.offset)
		if err := manager.runCommand([]string{"sudo", "incus", "config", "device", "set", staging, binding.device, listen}, nil, io.Discard, staging, instance.Name.String()); err != nil {
			return fmt.Errorf("rebind staged restore (retry restore): %w", err)
		}
	}
	if err := manager.runCommand([]string{"sudo", "incus", "config", "set", staging,
		"user.plateau.vnc-port=" + strconv.Itoa(int(instance.VNCPort)), "user.plateau.pending=restore", "user.plateau.restore-id=" + manifest.sourceID, "boot.autostart=false",
	}, nil, io.Discard, staging, instance.Name.String()); err != nil {
		return fmt.Errorf("record restore checkpoint: %w", err)
	}
	if err := manager.runCommand([]string{"sudo", "incus", "move", staging, incusName}, nil, io.Discard, staging, instance.Name.String()); err != nil {
		return fmt.Errorf("publish restored container (retry restore): %w", err)
	}
	return nil
}

func readPlateauBundle(bundlePath, payloadDestination string) (bundleManifest, error) {
	bundle, err := os.Open(bundlePath)
	if err != nil {
		return bundleManifest{}, fmt.Errorf("open Plateau backup: %w", err)
	}
	defer bundle.Close()
	manifest, err := readBundleManifest(bundle)
	if err != nil {
		return bundleManifest{}, err
	}
	// Hash exactly the compressed bytes being decoded, including the compression
	// trailer. The private output cannot reach Incus until validation succeeds.
	hash := sha256.New()
	payloadSource := io.TeeReader(bundle, hash)
	var decoded io.ReadCloser
	if manifest.FormatVersion == 1 {
		decoded, err = gzip.NewReader(payloadSource)
	} else {
		var decoder *zstd.Decoder
		decoder, err = zstd.NewReader(payloadSource, zstd.WithDecoderConcurrency(2), zstd.WithDecoderMaxMemory(64<<20))
		if err == nil {
			decoded = decoder.IOReadCloser()
		}
	}
	if err != nil {
		return bundleManifest{}, fmt.Errorf("open Incus export: %w", err)
	}
	defer decoded.Close()
	archive := tar.NewReader(decoded)
	// Incus reads both metadata copies during import. Quarantine them before
	// sending anything to the host: a crash immediately after import must never
	// expose an automatically started, unvalidated container on the next boot.
	// Only a private import copy is rewritten; the user's original bundle is unchanged.
	var payload *os.File
	var compressed *zstd.Encoder
	var prepared *tar.Writer
	if payloadDestination != "" {
		payload, err = os.OpenFile(payloadDestination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return bundleManifest{}, fmt.Errorf("create restore payload: %w", err)
		}
		defer payload.Close()
		// Bound compression buffers independently of the Mac CPU count. The
		// private copy uses zstd even for old gzip bundles to avoid slow VM inflate.
		compressed, err = zstd.NewWriter(payload,
			zstd.WithEncoderLevel(zstd.SpeedFastest),
			zstd.WithEncoderConcurrency(min(runtime.GOMAXPROCS(0), 4)),
			zstd.WithWindowSize(1<<20))
		if err != nil {
			return bundleManifest{}, fmt.Errorf("compress restore payload: %w", err)
		}
		defer compressed.Close()
		prepared = tar.NewWriter(compressed)
		defer prepared.Close()
	}
	entries := 0
	metadataSeen := make(map[string]bool, 2)
	// Hermes installations contain many small files. Reuse one copy buffer
	// rather than allocating another 32 KiB for every archive entry.
	copyBuffer := make([]byte, 32<<10)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return bundleManifest{}, fmt.Errorf("read Incus export: %w", err)
		}
		entries++
		if entries > 2_000_000 {
			return bundleManifest{}, fmt.Errorf("Incus export contains too many entries")
		}
		rawName := header.Name
		if header.Typeflag == tar.TypeDir {
			rawName = strings.TrimSuffix(rawName, "/")
		}
		cleanName := path.Clean(rawName)
		if cleanName != rawName || (cleanName != "backup" && !strings.HasPrefix(cleanName, "backup/")) {
			return bundleManifest{}, fmt.Errorf("Incus export contains unsafe path %q", header.Name)
		}
		var content io.Reader = archive
		if cleanName == "backup/index.yaml" || cleanName == "backup/container/backup.yaml" {
			if metadataSeen[cleanName] || header.Typeflag != tar.TypeReg || header.Size > 1<<20 {
				return bundleManifest{}, fmt.Errorf("invalid or duplicate Incus metadata %q", cleanName)
			}
			metadataSeen[cleanName] = true
			data, err := io.ReadAll(archive)
			if err != nil {
				return bundleManifest{}, fmt.Errorf("read Incus metadata: %w", err)
			}
			var document map[string]any
			decoder := yaml.NewDecoder(bytes.NewReader(data))
			if err := decoder.Decode(&document); err != nil || decoder.Decode(new(any)) != io.EOF {
				return bundleManifest{}, fmt.Errorf("invalid Incus metadata YAML in %q", cleanName)
			}
			backup := document
			if cleanName == "backup/index.yaml" {
				backup, _ = document["config"].(map[string]any)
			}
			container, _ := backup["container"].(map[string]any)
			if container["name"] != manifest.Instance.Name.String() {
				return bundleManifest{}, fmt.Errorf("Incus metadata name differs from backup manifest")
			}
			for _, key := range []string{"config", "expanded_config"} {
				if _, exists := container[key]; !exists && key == "expanded_config" {
					continue
				}
				config, ok := container[key].(map[string]any)
				if !ok || config["user.plateau.managed"] != "true" || config["user.plateau.name"] != manifest.Instance.Name.String() || config["user.plateau.desired"] != string(manifest.Instance.DesiredState) || config["user.plateau.vnc-port"] != strconv.Itoa(int(manifest.Instance.VNCPort)) {
					return bundleManifest{}, fmt.Errorf("Incus metadata ownership differs from backup manifest")
				}
				group, present := config["user.plateau.group"]
				if (!present && manifest.Instance.Group != "") || (present && group != manifest.Instance.Group) {
					return bundleManifest{}, fmt.Errorf("Incus metadata group differs from backup manifest")
				}
				delete(config, "user.plateau.rename-from")
				delete(config, "user.plateau.rename-to")
				delete(config, "user.plateau.rename-hosts")
				delete(config, "user.plateau.renamed-from")
				config["boot.autostart"] = "false"
				config["user.plateau.pending"] = "restore"
				config["user.plateau.restore-id"] = manifest.sourceID
			}
			if prepared != nil {
				data, err = yaml.Marshal(document)
				if err != nil {
					return bundleManifest{}, fmt.Errorf("encode quarantined Incus metadata: %w", err)
				}
				header.Size = int64(len(data))
				delete(header.PAXRecords, "size")
				content = bytes.NewReader(data)
			}
		}
		if prepared != nil {
			if err := prepared.WriteHeader(header); err != nil {
				return bundleManifest{}, fmt.Errorf("write import header: %w", err)
			}
			if _, err := io.CopyBuffer(prepared, content, copyBuffer); err != nil {
				return bundleManifest{}, fmt.Errorf("write import content: %w", err)
			}
		}
	}
	if len(metadataSeen) != 2 {
		return bundleManifest{}, fmt.Errorf("Incus export requires backup/index.yaml and backup/container/backup.yaml")
	}
	// Drain the decoder to verify its checksum after the tar trailer. Join any
	// read-ahead work before accessing the underlying stream and hash again.
	if _, err := io.Copy(io.Discard, decoded); err != nil {
		return bundleManifest{}, fmt.Errorf("verify Incus export compression: %w", err)
	}
	if err := decoded.Close(); err != nil {
		return bundleManifest{}, fmt.Errorf("finish Incus export decoding: %w", err)
	}
	if _, err := io.Copy(io.Discard, payloadSource); err != nil {
		return bundleManifest{}, fmt.Errorf("checksum Incus export: %w", err)
	}
	if !bytes.Equal(hash.Sum(nil), manifest.checksum) {
		return bundleManifest{}, fmt.Errorf("Incus export checksum mismatch")
	}
	if prepared != nil {
		if err := errors.Join(prepared.Close(), compressed.Close(), payload.Close()); err != nil {
			return bundleManifest{}, fmt.Errorf("finish restore payload: %w", err)
		}
	}
	return manifest, nil
}

func readBundleManifest(bundle io.Reader) (bundleManifest, error) {
	magic := make([]byte, len(bundleMagic))
	if _, err := io.ReadFull(bundle, magic); err != nil || (string(magic) != bundleMagic && string(magic) != legacyBundleMagic) {
		return bundleManifest{}, fmt.Errorf("invalid Plateau backup header")
	}
	var manifestSize uint32
	if err := binary.Read(bundle, binary.BigEndian, &manifestSize); err != nil || manifestSize == 0 || manifestSize > maximumManifestSize {
		return bundleManifest{}, fmt.Errorf("invalid Plateau backup manifest size")
	}
	manifestJSON := make([]byte, manifestSize)
	if _, err := io.ReadFull(bundle, manifestJSON); err != nil {
		return bundleManifest{}, fmt.Errorf("read Plateau backup manifest: %w", err)
	}
	var manifest bundleManifest
	decoder := json.NewDecoder(strings.NewReader(string(manifestJSON)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return bundleManifest{}, fmt.Errorf("invalid Plateau backup manifest")
	}
	if (manifest.FormatVersion != bundleFormatVersion || string(magic) != bundleMagic) && (manifest.FormatVersion != 1 || string(magic) != legacyBundleMagic) {
		return bundleManifest{}, fmt.Errorf("unsupported Plateau backup format %d", manifest.FormatVersion)
	}
	if err := manifest.Instance.Validate(); err != nil {
		return bundleManifest{}, fmt.Errorf("invalid Plateau backup instance: %w", err)
	}
	checksumLine := make([]byte, sha256.Size*2+1)
	if _, err := io.ReadFull(bundle, checksumLine); err != nil || checksumLine[len(checksumLine)-1] != '\n' {
		return bundleManifest{}, fmt.Errorf("invalid Plateau backup checksum")
	}
	expectedChecksum := string(checksumLine[:len(checksumLine)-1])
	decodedChecksum, err := hex.DecodeString(expectedChecksum)
	if err != nil || len(decodedChecksum) != sha256.Size {
		return bundleManifest{}, fmt.Errorf("invalid Plateau backup checksum")
	}
	sourceHash := sha256.New()
	_, _ = sourceHash.Write(manifestJSON)
	_, _ = sourceHash.Write(decodedChecksum)
	manifest.sourceID = hex.EncodeToString(sourceHash.Sum(nil))
	manifest.checksum = decodedChecksum
	return manifest, nil
}
