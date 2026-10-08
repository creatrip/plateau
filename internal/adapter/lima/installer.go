package lima

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/creatrip/plateau/internal/adapter/process"
)

const stableReleaseAPIURL = "https://api.github.com/repos/lima-vm/lima/releases/latest"

var stableVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

type stableRelease struct {
	version     string
	downloadURL string
	checksum    string
}

type Installer struct {
	ctx           context.Context
	runner        process.Runner
	stdout        io.Writer
	releaseAPIURL string
	goos          string
	goarch        string
	tarPath       string
	httpClient    *http.Client
}

func NewInstaller(ctx context.Context, stdout io.Writer) Installer {
	return Installer{
		ctx:           ctx,
		runner:        process.Runner{Context: ctx},
		stdout:        stdout,
		releaseAPIURL: stableReleaseAPIURL,
		goos:          runtime.GOOS,
		goarch:        runtime.GOARCH,
		tarPath:       "/usr/bin/tar",
		httpClient:    &http.Client{Timeout: 10 * time.Minute},
	}
}

func (installer Installer) Ensure() error {
	if err := installer.validatePlatform(); err != nil {
		return err
	}
	limaRoot, err := installer.limaRoot()
	if err != nil {
		return err
	}
	managedBinary := filepath.Join(limaRoot, "current", "bin", "limactl")
	if info, statErr := os.Stat(managedBinary); statErr == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		return installer.activate(managedBinary)
	}
	if systemBinary, lookPathErr := exec.LookPath("limactl"); lookPathErr == nil {
		if version, versionErr := installer.installedVersion(systemBinary); versionErr == nil {
			var major, minor, patch int
			if _, scanErr := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); scanErr == nil && (major > 2 || major == 2 && minor >= 2) {
				return nil
			}
		}
	}

	release, err := installer.latestStable()
	if err != nil {
		return err
	}
	var releaseMajor, releaseMinor, releasePatch int
	if _, err := fmt.Sscanf(release.version, "%d.%d.%d", &releaseMajor, &releaseMinor, &releasePatch); err != nil || releaseMajor < 2 || releaseMajor == 2 && releaseMinor < 2 {
		return fmt.Errorf("latest stable Lima %s does not support required autostart", release.version)
	}
	return installer.install(release, false)
}

func (installer Installer) Update() error {
	if err := installer.validatePlatform(); err != nil {
		return err
	}
	release, err := installer.latestStable()
	if err != nil {
		return err
	}
	// Match Ensure's selection order. Otherwise update may inspect a different
	// system installation from the managed executable used by normal commands.
	root, err := installer.limaRoot()
	if err != nil {
		return err
	}
	managedBinary := filepath.Join(root, "current", "bin", "limactl")
	if info, err := os.Stat(managedBinary); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		if err := installer.activate(managedBinary); err != nil {
			return err
		}
	}
	if limactlPath, lookPathErr := exec.LookPath("limactl"); lookPathErr == nil {
		if version, versionErr := installer.installedVersion(limactlPath); versionErr == nil && version == release.version {
			_, err := fmt.Fprintf(installer.stdout, "Lima %s is already the latest stable release.\n", release.version)
			return err
		}
	}
	return installer.install(release, true)
}

func (installer Installer) installedVersion(limactlPath string) (string, error) {
	output, err := installer.runner.Output(limactlPath, []string{"--version"})
	if err != nil {
		return "", fmt.Errorf("read Lima version: %w", err)
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return "", fmt.Errorf("read Lima version: empty output")
	}
	version := strings.TrimPrefix(fields[len(fields)-1], "v")
	if !stableVersionPattern.MatchString(version) {
		return "", fmt.Errorf("read Lima version: invalid version %q", version)
	}
	return version, nil
}

func (installer Installer) validatePlatform() error {
	if installer.goos != "darwin" || installer.goarch != "arm64" {
		return fmt.Errorf("Lima auto-install supports macOS arm64, got %s/%s", installer.goos, installer.goarch)
	}
	return nil
}

func (installer Installer) limaRoot() (string, error) {
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(homeDirectory, "Library", "Application Support", "plateau", "tools", "lima"), nil
}

func (installer Installer) latestStable() (stableRelease, error) {
	request, err := http.NewRequestWithContext(installer.ctx, http.MethodGet, installer.releaseAPIURL, nil)
	if err != nil {
		return stableRelease{}, fmt.Errorf("create Lima release request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("User-Agent", "plateau")
	response, err := installer.httpClient.Do(request)
	if err != nil {
		return stableRelease{}, fmt.Errorf("resolve latest stable Lima release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return stableRelease{}, fmt.Errorf("resolve latest stable Lima release: unexpected HTTP status %s", response.Status)
	}

	var metadata struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name               string `json:"name"`
			Digest             string `json:"digest"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		return stableRelease{}, fmt.Errorf("decode latest stable Lima release: %w", err)
	}
	if metadata.Draft || metadata.Prerelease || !strings.HasPrefix(metadata.TagName, "v") {
		return stableRelease{}, fmt.Errorf("latest Lima release is not stable")
	}
	version := strings.TrimPrefix(metadata.TagName, "v")
	if !stableVersionPattern.MatchString(version) {
		return stableRelease{}, fmt.Errorf("latest Lima release has invalid stable version %q", version)
	}
	assetName := fmt.Sprintf("lima-%s-Darwin-arm64.tar.gz", version)
	for _, asset := range metadata.Assets {
		if asset.Name != assetName {
			continue
		}
		checksum := strings.TrimPrefix(asset.Digest, "sha256:")
		checksumBytes, checksumErr := hex.DecodeString(checksum)
		downloadURL, downloadURLErr := url.ParseRequestURI(asset.BrowserDownloadURL)
		if !strings.HasPrefix(asset.Digest, "sha256:") || checksumErr != nil || len(checksumBytes) != sha256.Size || downloadURLErr != nil || downloadURL.Scheme != "https" {
			return stableRelease{}, fmt.Errorf("latest stable Lima release has invalid asset metadata")
		}
		return stableRelease{version: version, downloadURL: asset.BrowserDownloadURL, checksum: checksum}, nil
	}
	return stableRelease{}, fmt.Errorf("latest stable Lima release does not contain %s", assetName)
}

func (installer Installer) install(release stableRelease, updating bool) error {
	limaRoot, err := installer.limaRoot()
	if err != nil {
		return err
	}
	if updating {
		_, err = fmt.Fprintf(installer.stdout, "Updating Lima to %s...\n", release.version)
	} else {
		_, err = fmt.Fprintf(installer.stdout, "Installing Lima %s...\n", release.version)
	}
	if err != nil {
		return fmt.Errorf("write Lima installation status: %w", err)
	}
	if err := os.MkdirAll(limaRoot, 0o755); err != nil {
		return fmt.Errorf("create Lima tools directory: %w", err)
	}
	installDirectory := filepath.Join(limaRoot, release.version)
	limactlPath := filepath.Join(installDirectory, "bin", "limactl")
	info, statErr := os.Stat(limactlPath)
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		if err := os.RemoveAll(installDirectory); err != nil {
			return fmt.Errorf("remove incomplete Lima installation: %w", err)
		}
		temporaryDirectory, err := os.MkdirTemp(limaRoot, ".install-*")
		if err != nil {
			return fmt.Errorf("create Lima installation directory: %w", err)
		}
		defer os.RemoveAll(temporaryDirectory)

		request, err := http.NewRequestWithContext(installer.ctx, http.MethodGet, release.downloadURL, nil)
		if err != nil {
			return fmt.Errorf("create Lima download request: %w", err)
		}
		request.Header.Set("User-Agent", "plateau")
		response, err := installer.httpClient.Do(request)
		if err != nil {
			return fmt.Errorf("download Lima: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("download Lima: unexpected HTTP status %s", response.Status)
		}

		archivePath := filepath.Join(temporaryDirectory, "lima.tar.gz")
		archiveFile, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("create Lima archive: %w", err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(archiveFile, hash), response.Body)
		closeErr := archiveFile.Close()
		if copyErr != nil {
			return fmt.Errorf("save Lima archive: %w", copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close Lima archive: %w", closeErr)
		}
		if actualChecksum := fmt.Sprintf("%x", hash.Sum(nil)); actualChecksum != release.checksum {
			return fmt.Errorf("verify Lima checksum: got %s, want %s", actualChecksum, release.checksum)
		}

		extractedDirectory := filepath.Join(temporaryDirectory, "root")
		if err := os.Mkdir(extractedDirectory, 0o755); err != nil {
			return fmt.Errorf("create Lima extraction directory: %w", err)
		}
		if output, err := installer.runner.Output(installer.tarPath, []string{"-xzf", archivePath, "-C", extractedDirectory}); err != nil {
			return fmt.Errorf("extract Lima: %w: %s", err, output)
		}
		extractedBinary := filepath.Join(extractedDirectory, "bin", "limactl")
		if info, err := os.Stat(extractedBinary); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("validate extracted Lima binary")
		}
		if err := os.Rename(extractedDirectory, installDirectory); err != nil {
			if info, statErr := os.Stat(limactlPath); statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				return fmt.Errorf("install Lima: %w", err)
			}
		}
	}

	currentLink := filepath.Join(limaRoot, "current")
	temporaryLink := filepath.Join(limaRoot, fmt.Sprintf(".current-%d", time.Now().UnixNano()))
	if err := os.Symlink(release.version, temporaryLink); err != nil {
		return fmt.Errorf("prepare current Lima version: %w", err)
	}
	defer os.Remove(temporaryLink)
	if err := os.Rename(temporaryLink, currentLink); err != nil {
		return fmt.Errorf("activate current Lima version: %w", err)
	}
	if err := installer.activate(limactlPath); err != nil {
		return err
	}
	if updating {
		_, err = fmt.Fprintf(installer.stdout, "Updated Lima to %s.\n", release.version)
	} else {
		_, err = fmt.Fprintf(installer.stdout, "Installed Lima %s.\n", release.version)
	}
	return err
}

func (installer Installer) activate(limactlPath string) error {
	binDirectory := filepath.Dir(limactlPath)
	path := binDirectory
	if currentPath := os.Getenv("PATH"); currentPath != "" {
		path += string(os.PathListSeparator) + currentPath
	}
	if err := os.Setenv("PATH", path); err != nil {
		return fmt.Errorf("activate managed Lima: %w", err)
	}
	return nil
}
