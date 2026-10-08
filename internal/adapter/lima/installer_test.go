package lima

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInstallerUsesManagedLimaBeforeSystemLima(t *testing.T) {
	homeDirectory := t.TempDir()
	versionCheckMarker := filepath.Join(homeDirectory, "version-checked")
	managedBinary := filepath.Join(homeDirectory, "Library", "Application Support", "plateau", "tools", "lima", "2.2.0", "bin", "limactl")
	if err := os.MkdirAll(filepath.Dir(managedBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managedBinary, []byte(fmt.Sprintf("#!/bin/sh\nprintf x > %q\necho 'limactl version 2.2.0'\n", versionCheckMarker)), 0o755); err != nil {
		t.Fatal(err)
	}
	currentLink := filepath.Join(homeDirectory, "Library", "Application Support", "plateau", "tools", "lima", "current")
	if err := os.Symlink("2.2.0", currentLink); err != nil {
		t.Fatal(err)
	}
	systemBinDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(systemBinDirectory, "limactl"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDirectory)
	t.Setenv("PATH", systemBinDirectory)
	installer := NewInstaller(context.Background(), &bytes.Buffer{})

	err := installer.Ensure()

	if err != nil {
		t.Fatalf("ensure dependencies: %v", err)
	}
	foundPath, err := exec.LookPath("limactl")
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath, err := filepath.EvalSymlinks(foundPath)
	if err != nil {
		t.Fatal(err)
	}
	resolvedManagedBinary, err := filepath.EvalSymlinks(managedBinary)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedPath != resolvedManagedBinary {
		t.Fatalf("limactl path = %q, want managed path %q", resolvedPath, resolvedManagedBinary)
	}
	if _, err := os.Stat(versionCheckMarker); !os.IsNotExist(err) {
		t.Fatalf("managed Lima version check marker exists: %v", err)
	}
}

func TestInstallerUpdateActivatesExistingLatestManagedLima(t *testing.T) {
	archive := buildLimaArchive(t, "2.3.0")
	server, requests := serveStableRelease(t, "2.3.0", archive, fmt.Sprintf("%x", sha256.Sum256(archive)))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	installer := testInstaller(&bytes.Buffer{}, server, "/usr/bin/tar")
	root, err := installer.limaRoot()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "2.3.0", "bin", "limactl")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 'limactl version 2.3.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("2.3.0", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := installer.Update(); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("downloaded an already-installed release %d times", requests.Load())
	}
	if found, err := exec.LookPath("limactl"); err != nil || found != filepath.Join(root, "current", "bin", "limactl") {
		t.Fatalf("active binary=%q err=%v", found, err)
	}
}

func TestInstallerUsesSystemLimaWithoutCheckingForUpdates(t *testing.T) {
	binDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDirectory, "limactl"), []byte("#!/bin/sh\necho 'limactl version 2.2.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", binDirectory)
	installer := NewInstaller(context.Background(), &bytes.Buffer{})
	installer.releaseAPIURL = "http://127.0.0.1:1"

	err := installer.Ensure()

	if err != nil {
		t.Fatalf("ensure dependencies: %v", err)
	}
}

func TestInstallerReplacesSystemLimaOlderThanAutostartMinimum(t *testing.T) {
	archive := buildLimaArchive(t, "2.3.0")
	server, _ := serveStableRelease(t, "2.3.0", archive, fmt.Sprintf("%x", sha256.Sum256(archive)))
	homeDirectory := t.TempDir()
	systemBinDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(systemBinDirectory, "limactl"), []byte("#!/bin/sh\necho 'limactl version 2.1.4'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tarPath, err := exec.LookPath("tar")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDirectory)
	t.Setenv("PATH", systemBinDirectory)
	installer := testInstaller(&bytes.Buffer{}, server, tarPath)

	if err := installer.Ensure(); err != nil {
		t.Fatalf("ensure dependencies: %v", err)
	}
	foundPath, err := exec.LookPath("limactl")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(homeDirectory, "Library", "Application Support", "plateau", "tools", "lima", "2.3.0", "bin", "limactl")
	if foundPath != want {
		t.Fatalf("limactl path = %q, want managed %q", foundPath, want)
	}
}

func TestInstallerRejectsUnsupportedHostEvenWhenLimaIsOnPath(t *testing.T) {
	binDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDirectory, "limactl"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory)
	installer := NewInstaller(context.Background(), &bytes.Buffer{})
	installer.goos = "linux"
	installer.goarch = "arm64"

	err := installer.Ensure()

	if err == nil || !strings.Contains(err.Error(), "supports macOS arm64") {
		t.Fatalf("error = %v, want unsupported host error", err)
	}
}

func TestInstallerInstallsLatestStableReleaseWhenLimaIsMissing(t *testing.T) {
	archive := buildLimaArchive(t, "2.3.0")
	server, _ := serveStableRelease(t, "2.3.0", archive, fmt.Sprintf("%x", sha256.Sum256(archive)))
	homeDirectory := t.TempDir()
	tarPath, err := exec.LookPath("tar")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDirectory)
	t.Setenv("PATH", t.TempDir())
	var output bytes.Buffer
	installer := testInstaller(&output, server, tarPath)

	err = installer.Ensure()

	if err != nil {
		t.Fatalf("ensure dependencies: %v", err)
	}
	installedBinary := filepath.Join(homeDirectory, "Library", "Application Support", "plateau", "tools", "lima", "2.3.0", "bin", "limactl")
	foundPath, err := exec.LookPath("limactl")
	if err != nil {
		t.Fatalf("find installed limactl: %v", err)
	}
	if foundPath != installedBinary {
		t.Fatalf("limactl path = %q, want %q", foundPath, installedBinary)
	}
	if !strings.Contains(output.String(), "Installing Lima 2.3.0") {
		t.Fatalf("output = %q, want latest stable installation", output.String())
	}
}

func TestInstallerUpdatesOlderLimaToLatestStableRelease(t *testing.T) {
	archive := buildLimaArchive(t, "2.3.0")
	server, _ := serveStableRelease(t, "2.3.0", archive, fmt.Sprintf("%x", sha256.Sum256(archive)))
	homeDirectory := t.TempDir()
	tarPath, err := exec.LookPath("tar")
	if err != nil {
		t.Fatal(err)
	}
	oldManagedBinary := filepath.Join(homeDirectory, "Library", "Application Support", "plateau", "tools", "lima", "2.1.4", "bin", "limactl")
	if err := os.MkdirAll(filepath.Dir(oldManagedBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldManagedBinary, []byte("#!/bin/sh\necho 'limactl version 2.1.4'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	currentLink := filepath.Join(homeDirectory, "Library", "Application Support", "plateau", "tools", "lima", "current")
	if err := os.Symlink("2.1.4", currentLink); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDirectory)
	t.Setenv("PATH", t.TempDir())
	var output bytes.Buffer
	installer := testInstaller(&output, server, tarPath)
	err = installer.Update()

	if err != nil {
		t.Fatalf("update dependencies: %v", err)
	}
	installedBinary := filepath.Join(homeDirectory, "Library", "Application Support", "plateau", "tools", "lima", "2.3.0", "bin", "limactl")
	foundPath, err := exec.LookPath("limactl")
	if err != nil {
		t.Fatalf("find updated limactl: %v", err)
	}
	if foundPath != installedBinary {
		t.Fatalf("limactl path = %q, want %q", foundPath, installedBinary)
	}
	currentTarget, err := os.Readlink(currentLink)
	if err != nil {
		t.Fatalf("read current Lima link: %v", err)
	}
	if currentTarget != "2.3.0" {
		t.Fatalf("current Lima target = %q, want %q", currentTarget, "2.3.0")
	}
	if !strings.Contains(output.String(), "Updating Lima to 2.3.0") {
		t.Fatalf("output = %q, want stable update progress", output.String())
	}
}

func TestInstallerDoesNotDownloadWhenLimaIsLatestStable(t *testing.T) {
	archive := buildLimaArchive(t, "2.3.0")
	server, archiveRequests := serveStableRelease(t, "2.3.0", archive, fmt.Sprintf("%x", sha256.Sum256(archive)))
	systemBinDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(systemBinDirectory, "limactl"), []byte("#!/bin/sh\necho 'limactl version 2.3.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", systemBinDirectory)
	var output bytes.Buffer
	installer := testInstaller(&output, server, "/usr/bin/tar")

	err := installer.Update()

	if err != nil {
		t.Fatalf("update dependencies: %v", err)
	}
	if archiveRequests.Load() != 0 {
		t.Fatalf("archive requests = %d, want 0", archiveRequests.Load())
	}
	if output.String() != "Lima 2.3.0 is already the latest stable release.\n" {
		t.Fatalf("output = %q, want already-latest result", output.String())
	}
}

func TestInstallerRejectsArchiveWithWrongChecksum(t *testing.T) {
	archive := buildLimaArchive(t, "2.3.0")
	server, _ := serveStableRelease(t, "2.3.0", archive, strings.Repeat("0", 64))
	homeDirectory := t.TempDir()
	tarPath, err := exec.LookPath("tar")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDirectory)
	t.Setenv("PATH", t.TempDir())
	installer := testInstaller(&bytes.Buffer{}, server, tarPath)

	err = installer.Ensure()

	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error = %v, want checksum failure", err)
	}
	installedBinary := filepath.Join(homeDirectory, "Library", "Application Support", "plateau", "tools", "lima", "2.3.0", "bin", "limactl")
	if _, statErr := os.Stat(installedBinary); !os.IsNotExist(statErr) {
		t.Fatalf("installed limactl after checksum failure: %v", statErr)
	}
}

func TestInstallerRejectsInvalidStableVersion(t *testing.T) {
	archive := buildLimaArchive(t, "2.3.0")
	server, _ := serveStableRelease(t, "../../2.3.0", archive, fmt.Sprintf("%x", sha256.Sum256(archive)))
	tarPath, err := exec.LookPath("tar")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	installer := testInstaller(&bytes.Buffer{}, server, tarPath)

	err = installer.Ensure()

	if err == nil || !strings.Contains(err.Error(), "invalid stable version") {
		t.Fatalf("error = %v, want invalid stable version", err)
	}
}

func testInstaller(output *bytes.Buffer, server *httptest.Server, tarPath string) Installer {
	installer := NewInstaller(context.Background(), output)
	installer.releaseAPIURL = server.URL + "/release"
	installer.goos, installer.goarch = "darwin", "arm64"
	installer.tarPath, installer.httpClient = tarPath, server.Client()
	return installer
}

func serveStableRelease(t *testing.T, version string, archive []byte, checksum string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var archiveRequests atomic.Int32
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/release":
			response.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(response, `{"tag_name":"v%s","draft":false,"prerelease":false,"assets":[{"name":"lima-%s-Darwin-arm64.tar.gz","digest":"sha256:%s","browser_download_url":"%s/archive"}]}`, version, version, checksum, server.URL)
		case "/archive":
			archiveRequests.Add(1)
			_, _ = response.Write(archive)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	return server, &archiveRequests
}

func buildLimaArchive(t *testing.T, version string) []byte {
	t.Helper()
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	content := []byte("#!/bin/sh\necho 'limactl version " + version + "'\n")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "bin/limactl", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
