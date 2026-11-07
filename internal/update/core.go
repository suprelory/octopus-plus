package update

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/shutdown"
)

var updateInProgress atomic.Bool

func UpdateCore() error {
	if !updateInProgress.CompareAndSwap(false, true) {
		return fmt.Errorf("self-update is already in progress")
	}
	releaseUpdateLock := true
	defer func() {
		if releaseUpdateLock {
			updateInProgress.Store(false)
		}
	}()
	log.Infof("start update core")

	if _, err := decodePublicKey(releasePublicKey); err != nil {
		return err
	}
	filename, err := getDownloadFilename()
	if err != nil {
		return err
	}
	manifest, err := doRequestWithFallback(updateURL+"/"+checksumManifestName, maxManifestBytes)
	if err != nil {
		return fmt.Errorf("download checksum manifest: %w", err)
	}
	signature, err := doRequestWithFallback(updateURL+"/"+manifestSignatureName, maxSignatureBytes)
	if err != nil {
		return fmt.Errorf("download checksum signature: %w", err)
	}
	expectedChecksum, err := verifyReleaseManifest(filename, manifest, signature, releasePublicKey)
	if err != nil {
		return fmt.Errorf("verify update release: %w", err)
	}
	archive, err := doRequestWithFallback(updateURL+"/"+filename, maxUpdateArchiveBytes)
	if err != nil {
		return fmt.Errorf("download update archive: %w", err)
	}
	if err := verifyArtifactChecksum(filename, archive, expectedChecksum); err != nil {
		return fmt.Errorf("verify update release: %w", err)
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	stagingDir, err := os.MkdirTemp(filepath.Dir(execPath), ".octopus-update-")
	if err != nil {
		return fmt.Errorf("create update staging directory: %w", err)
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	expectedExecutable := "octopus"
	if runtime.GOOS == "windows" {
		expectedExecutable += ".exe"
	}
	stagedExecutable, err := extractUpdateArchive(archive, stagingDir, expectedExecutable)
	if err != nil {
		return err
	}
	if err := validateExecutable(stagedExecutable, runtime.GOOS, runtime.GOARCH); err != nil {
		return fmt.Errorf("validate staged executable: %w", err)
	}
	backupPath, err := prepareExecutableInstall(stagedExecutable, execPath)
	if err != nil {
		return fmt.Errorf("prepare update install: %w", err)
	}

	log.Infow("update.prepared", "artifact", filename, "backup", backupPath)
	cleanupStaging = false
	releaseUpdateLock = false
	go restartExecutable(execPath, stagedExecutable, backupPath, stagingDir)
	return nil
}

func getDownloadFilename() (string, error) {
	arch := runtime.GOARCH
	goos := runtime.GOOS

	switch goos {
	case "windows":
		switch arch {
		case "386":
			return "octopus-windows-x86.zip", nil
		case "amd64":
			return "octopus-windows-x86_64.zip", nil
		}
	case "darwin":
		switch arch {
		case "amd64":
			return "octopus-darwin-x86_64.zip", nil
		case "arm64":
			return "octopus-darwin-arm64.zip", nil
		}
	case "linux":
		switch arch {
		case "386":
			return "octopus-linux-x86.zip", nil
		case "amd64":
			return "octopus-linux-x86_64.zip", nil
		case "arm":
			return "octopus-linux-armv7.zip", nil
		case "arm64":
			return "octopus-linux-arm64.zip", nil
		}
	}
	return "", fmt.Errorf("unsupported platform: %s/%s", goos, arch)
}

func validateExecutable(path, goos, arch string) error {
	switch goos {
	case "linux":
		file, err := elf.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		expected := map[string]elf.Machine{"386": elf.EM_386, "amd64": elf.EM_X86_64, "arm": elf.EM_ARM, "arm64": elf.EM_AARCH64}[arch]
		if expected == elf.EM_NONE || file.Machine != expected {
			return fmt.Errorf("ELF machine %s does not match %s", file.Machine, arch)
		}
	case "darwin":
		file, err := macho.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		expected := map[string]macho.Cpu{"amd64": macho.CpuAmd64, "arm64": macho.CpuArm64}[arch]
		if expected == 0 || file.Cpu != expected {
			return fmt.Errorf("Mach-O CPU %s does not match %s", file.Cpu, arch)
		}
	case "windows":
		file, err := pe.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		expected := map[string]uint16{"386": pe.IMAGE_FILE_MACHINE_I386, "amd64": pe.IMAGE_FILE_MACHINE_AMD64}[arch]
		if expected == 0 || file.Machine != expected {
			return fmt.Errorf("PE machine %#x does not match %s", file.Machine, arch)
		}
	default:
		return fmt.Errorf("unsupported executable platform %s", goos)
	}
	return nil
}

func prepareExecutableInstall(stagedPath, execPath string) (string, error) {
	currentInfo, err := os.Stat(execPath)
	if err != nil {
		return "", fmt.Errorf("stat current executable: %w", err)
	}
	if err := os.Chmod(stagedPath, currentInfo.Mode().Perm()); err != nil {
		return "", fmt.Errorf("set executable permissions: %w", err)
	}
	backupPath := execPath + ".rollback"
	return backupPath, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open executable directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("sync executable directory: %w", err)
	}
	return nil
}

func restartExecutable(execPath, stagedPath, backupPath, stagingDir string) {
	defer updateInProgress.Store(false)
	defer func() {
		if err := os.RemoveAll(stagingDir); err != nil {
			log.Warnf("remove update staging directory %s: %v", stagingDir, err)
		}
	}()
	if err := restartWithUpdate(execPath, stagedPath, backupPath, stagingDir); err != nil {
		log.Errorf("restarting with update failed: %v; rollback executable retained at %s", err, backupPath)
		return
	}
	if runtime.GOOS == "windows" {
		shutdown.Shutdown()
		os.Exit(0)
	}
}
