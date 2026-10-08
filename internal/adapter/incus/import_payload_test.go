package incus

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creatrip/plateau/internal/domain"
	"github.com/klauspost/compress/zstd"
	"go.yaml.in/yaml/v3"
)

func TestRestoreStreamsValidatedPayload(t *testing.T) {
	manager, transport := newManagerTest(t)
	instance := domain.Instance{Name: "stream", VNCPort: 30000, DesiredState: domain.DesiredStopped}
	transport.remoteFile = incusExportPayload(t, instance)
	bundle := filepath.Join(t.TempDir(), "backup.plateau")
	if err := manager.Backup(instance, bundle); err != nil {
		t.Fatal(err)
	}
	manifest, err := readPlateauBundle(bundle, "")
	if err != nil {
		t.Fatal(err)
	}
	staging := "plateau-import-" + manifest.sourceID[:24]
	key := "sudo\x00incus\x00list\x00^" + staging + "$\x00--format=json"
	staged := []byte(fmt.Sprintf(`[{"name":%q,"type":"container","status":"Stopped","config":{"user.plateau.managed":"true","user.plateau.name":"stream","user.plateau.desired":"stopped","user.plateau.vnc-port":"30000","boot.autostart":"false","user.plateau.pending":"restore","user.plateau.restore-id":%q},"devices":{"plateau-vnc":{"type":"proxy","connect":"tcp:127.0.0.1:6080"}}}]`, staging, manifest.sourceID))
	transport.outputSequences = map[string][][]byte{key: {[]byte("[]"), staged}}
	if err := manager.RestoreBackup(bundle, instance); err != nil {
		t.Fatal(err)
	}
	if len(transport.copies) != 0 {
		t.Fatalf("restore still writes an extra host copy: %v", transport.copies)
	}
	for _, call := range transport.calls {
		if strings.Join(call.args, " ") != "sudo incus import - "+staging {
			continue
		}
		if call.stdin == "" {
			t.Fatal("import received no validated payload")
		}
		return
	}
	t.Fatal("restore did not stream the validated archive into Incus")
}

func TestRestoreRejectsCorruptPayloadBeforeContactingHost(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("V%d", version), func(t *testing.T) {
			for _, corruption := range []string{"checksum", "compression trailer", "truncated", "metadata"} {
				t.Run(corruption, func(t *testing.T) {
					manager, transport := newManagerTest(t)
					instance := domain.Instance{Name: "corrupt", VNCPort: 30000, DesiredState: domain.DesiredStopped}
					transport.remoteFile = incusExportPayload(t, instance, version == 1)
					bundle := filepath.Join(t.TempDir(), "backup.plateau")
					if corruption == "metadata" {
						other := instance
						other.Group = "different"
						transport.remoteFile = incusExportPayload(t, other, version == 1)
					}
					if err := manager.Backup(instance, bundle); err != nil {
						t.Fatal(err)
					}
					data, err := os.ReadFile(bundle)
					if err != nil {
						t.Fatal(err)
					}
					if version == 1 {
						data = bytes.Replace(data, []byte(bundleMagic), []byte(legacyBundleMagic), 1)
						data = bytes.Replace(data, []byte(`"formatVersion":2`), []byte(`"formatVersion":1`), 1)
					}
					checksumOffset := len(bundleMagic) + 4 + int(binary.BigEndian.Uint32(data[len(bundleMagic):]))
					payloadOffset := checksumOffset + sha256.Size*2 + 1
					switch corruption {
					case "checksum":
						copy(data[checksumOffset:], strings.Repeat("0", sha256.Size*2))
					case "compression trailer":
						data[len(data)-1] ^= 0xff
						digest := sha256.Sum256(data[payloadOffset:])
						copy(data[checksumOffset:], hex.EncodeToString(digest[:]))
					case "truncated":
						data = data[:len(data)-8]
						digest := sha256.Sum256(data[payloadOffset:])
						copy(data[checksumOffset:], hex.EncodeToString(digest[:]))
					}
					if err := os.WriteFile(bundle, data, 0o600); err != nil {
						t.Fatal(err)
					}
					// Reading the small manifest must not scan or trust the payload.
					got, err := manager.ReadBackupManifest(bundle)
					if err != nil || got != instance {
						t.Fatalf("manifest = %+v, %v", got, err)
					}
					workspace := t.TempDir()
					t.Setenv("TMPDIR", workspace)
					before := len(transport.calls)
					if err := manager.RestoreBackup(bundle, instance); err == nil {
						t.Fatal("corrupt archive accepted")
					}
					if len(transport.calls) != before || len(transport.copies) != 0 {
						t.Fatal("unverified data reached the host")
					}
					entries, err := os.ReadDir(workspace)
					if err != nil || len(entries) != 0 {
						t.Fatalf("partial restore workspace retained: %v, %v", entries, err)
					}
				})
			}
		})
	}
}

func TestImportPayloadIsQuarantinedBeforeIncusSeesIt(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("V%d", version), func(t *testing.T) {
			manager, transport := newManagerTest(t)
			instance := domain.Instance{Name: "a", VNCPort: 30000, DesiredState: domain.DesiredRunning}
			transport.remoteFile = incusExportPayload(t, instance, version == 1)
			path := filepath.Join(t.TempDir(), "backup.plateau")
			if err := manager.Backup(instance, path); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if version == 1 {
				original = bytes.Replace(original, []byte(bundleMagic), []byte(legacyBundleMagic), 1)
				original = bytes.Replace(original, []byte(`"formatVersion":2`), []byte(`"formatVersion":1`), 1)
				if err := os.WriteFile(path, original, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			prepared := filepath.Join(t.TempDir(), "import.tar.zst")
			manifest, err := readPlateauBundle(path, prepared)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(prepared)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			decoder, err := zstd.NewReader(file)
			if err != nil {
				t.Fatal(err)
			}
			defer decoder.Close()
			archive := tar.NewReader(decoder)
			checked := 0
			payloadSeen := false
			linksSeen := 0
			for {
				header, err := archive.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				content, err := io.ReadAll(archive)
				if err != nil {
					t.Fatal(err)
				}
				if header.Name == "backup/container/rootfs/probe" {
					payloadSeen = string(content) == "untouched workload bytes"
					if header.Mode != 0640 || header.Uid != 1000 || header.Gid != 1000 || header.ModTime.Unix() != 1700000000 || header.Xattrs["user.plateau-probe"] != "retained attribute" {
						t.Fatalf("workload attributes changed: %+v", header)
					}
					continue
				}
				if strings.HasSuffix(header.Name, "/probe-symlink") || strings.HasSuffix(header.Name, "/probe-hardlink") {
					wantType, wantTarget := byte(tar.TypeSymlink), "probe"
					if strings.HasSuffix(header.Name, "/probe-hardlink") {
						wantType, wantTarget = tar.TypeLink, "backup/container/rootfs/probe"
					}
					if header.Typeflag != wantType || header.Linkname != wantTarget || header.Uid != 1000 || header.Gid != 1000 {
						t.Fatalf("workload link changed: %+v", header)
					}
					linksSeen++
					continue
				}
				var document map[string]any
				if err := yaml.Unmarshal(content, &document); err != nil {
					t.Fatal(err)
				}
				if header.Name == "backup/index.yaml" {
					document = document["config"].(map[string]any)
				}
				container := document["container"].(map[string]any)
				if container["description"] != "retained metadata" {
					t.Fatal("unknown metadata was lost")
				}
				for _, key := range []string{"config", "expanded_config"} {
					config := container[key].(map[string]any)
					if config["boot.autostart"] != "false" || config["user.plateau.pending"] != "restore" || config["user.plateau.restore-id"] != manifest.sourceID {
						t.Fatalf("unsafe import metadata: %v", config)
					}
				}
				checked++
			}
			if checked != 2 || !payloadSeen || linksSeen != 2 {
				t.Fatalf("checked=%d payload=%v links=%d", checked, payloadSeen, linksSeen)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, after) {
				t.Fatal("original backup was modified")
			}
		})
	}
}

func TestBundleRejectsMismatchedFormatAndCompression(t *testing.T) {
	for _, corruption := range []string{"magic", "version", "unknown version", "gzip in V2", "zstd in V1"} {
		t.Run(corruption, func(t *testing.T) {
			manager, transport := newManagerTest(t)
			instance := domain.Instance{Name: "format", VNCPort: 30000, DesiredState: domain.DesiredStopped}
			transport.remoteFile = incusExportPayload(t, instance, corruption == "gzip in V2")
			bundle := filepath.Join(t.TempDir(), "backup.plateau")
			if err := manager.Backup(instance, bundle); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(bundle)
			if err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "magic":
				data = bytes.Replace(data, []byte(bundleMagic), []byte(legacyBundleMagic), 1)
			case "version":
				data = bytes.Replace(data, []byte(`"formatVersion":2`), []byte(`"formatVersion":1`), 1)
			case "unknown version":
				data = bytes.Replace(data, []byte(`"formatVersion":2`), []byte(`"formatVersion":3`), 1)
			case "zstd in V1":
				data = bytes.Replace(data, []byte(bundleMagic), []byte(legacyBundleMagic), 1)
				data = bytes.Replace(data, []byte(`"formatVersion":2`), []byte(`"formatVersion":1`), 1)
			}
			if err := os.WriteFile(bundle, data, 0o600); err != nil {
				t.Fatal(err)
			}
			before := len(transport.calls)
			if err := manager.RestoreBackup(bundle, instance); err == nil {
				t.Fatal("mismatched format was accepted")
			}
			if len(transport.calls) != before {
				t.Fatal("mismatched format reached the host")
			}
		})
	}
}
