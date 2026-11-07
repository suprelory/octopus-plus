package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/client"
	"github.com/suprelory/octopus-plus/internal/conf"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

const (
	updateURL             = "https://github.com/suprelory/octopus-plus/releases/latest/download"
	updateAPIURL          = "https://api.github.com/repos/suprelory/octopus-plus/releases/latest"
	checksumManifestName  = "checksums.sha256"
	manifestSignatureName = "checksums.sha256.sig"
	maxReleaseInfoBytes   = 2 << 20
	maxManifestBytes      = 1 << 20
	maxSignatureBytes     = 16 << 10
	maxUpdateArchiveBytes = 256 << 20
	maxArchiveFiles       = 8
	maxExtractedBytes     = 512 << 20
)

var (
	githubPAT = os.Getenv(strings.ToUpper(conf.APP_NAME) + "_GITHUB_PAT")
	// releasePublicKey is injected at release build time with -X as raw/base64 Ed25519 bytes.
	// Keeping an empty development default makes unsigned local builds fail closed.
	releasePublicKey    string
	errResponseTooLarge = errors.New("response exceeds configured size limit")
)

type LatestInfo struct {
	TagName     string `json:"tag_name"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"`
	Message     string `json:"message"`
}

func doRequestWithFallback(url string, limit int64) ([]byte, error) {
	data, err := doRequest(url, false, limit)
	if err == nil {
		return data, nil
	}
	log.Warnf("direct request failed, trying with proxy: %v", err)
	return doRequest(url, true, limit)
}

func doRequest(url string, useProxy bool, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hc, err := client.GetHTTPClientSystemProxy(useProxy)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create update request: %w", err)
	}
	if githubPAT != "" {
		req.Header.Set("Authorization", "Bearer "+githubPAT)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request update artifact: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := readAllLimit(resp.Body, 64<<10)
		return nil, fmt.Errorf("update server returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if resp.ContentLength > limit && resp.ContentLength >= 0 {
		return nil, fmt.Errorf("update response content length %d exceeds limit %d: %w", resp.ContentLength, limit, errResponseTooLarge)
	}

	data, err := readAllLimit(resp.Body, limit)
	if err != nil {
		return nil, fmt.Errorf("read update response: %w", err)
	}
	return data, nil
}

func readAllLimit(reader io.Reader, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, fmt.Errorf("invalid response limit %d", limit)
	}
	limited := io.LimitReader(reader, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errResponseTooLarge
	}
	return data, nil
}

func GetLatestInfo() (*LatestInfo, error) {
	body, err := doRequestWithFallback(updateAPIURL, maxReleaseInfoBytes)
	if err != nil {
		return nil, err
	}

	var latestInfo LatestInfo
	if err := json.Unmarshal(body, &latestInfo); err != nil {
		return nil, fmt.Errorf("decode latest release: %w", err)
	}
	if latestInfo.Message != "" {
		return nil, fmt.Errorf("failed to get latest info: %s", latestInfo.Message)
	}
	return &latestInfo, nil
}

func verifyReleaseArtifact(filename string, archive, manifest, signature []byte, publicKey string) error {
	expected, err := verifyReleaseManifest(filename, manifest, signature, publicKey)
	if err != nil {
		return err
	}
	return verifyArtifactChecksum(filename, archive, expected)
}

func verifyReleaseManifest(filename string, manifest, signature []byte, publicKey string) ([]byte, error) {
	key, err := decodePublicKey(publicKey)
	if err != nil {
		return nil, err
	}
	sig, err := decodeSignature(signature)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(key, manifest, sig) {
		return nil, errors.New("release checksum manifest signature is invalid")
	}

	expected, err := checksumForFile(manifest, filename)
	if err != nil {
		return nil, err
	}
	return expected, nil
}

func verifyArtifactChecksum(filename string, archive, expected []byte) error {
	if len(expected) != sha256.Size {
		return fmt.Errorf("invalid expected SHA-256 digest length %d", len(expected))
	}
	actual := sha256.Sum256(archive)
	if !bytes.Equal(actual[:], expected) {
		return fmt.Errorf("release checksum mismatch for %s", filename)
	}
	return nil
}

func decodePublicKey(encoded string) (ed25519.PublicKey, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errors.New("self-update is disabled: release verification public key is not embedded")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("decode release public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("release public key length %d, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

func decodeSignature(data []byte) ([]byte, error) {
	trimmed := strings.TrimSpace(string(data))
	if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
		if len(decoded) != ed25519.SignatureSize {
			return nil, fmt.Errorf("release signature length %d, want %d", len(decoded), ed25519.SignatureSize)
		}
		return decoded, nil
	}
	if len(data) == ed25519.SignatureSize {
		return data, nil
	}
	return nil, errors.New("release signature is not valid base64 Ed25519 data")
}

func checksumForFile(manifest []byte, filename string) ([]byte, error) {
	if filepath.Base(filename) != filename || strings.ContainsAny(filename, "/\\") || filename == "." || filename == "" {
		return nil, fmt.Errorf("invalid artifact filename %q", filename)
	}
	var found []byte
	for lineNumber, line := range strings.Split(string(manifest), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid checksum manifest line %d", lineNumber+1)
		}
		name := strings.TrimPrefix(fields[1], "*")
		if filepath.Base(name) != name || strings.ContainsAny(name, "/\\") || name == "." {
			return nil, fmt.Errorf("invalid checksum filename on line %d", lineNumber+1)
		}
		digest, err := hex.DecodeString(fields[0])
		if err != nil || len(digest) != sha256.Size {
			return nil, fmt.Errorf("invalid SHA-256 digest on line %d", lineNumber+1)
		}
		if name == filename {
			if found != nil {
				return nil, fmt.Errorf("duplicate checksum entry for %s", filename)
			}
			found = digest
		}
	}
	if found == nil {
		return nil, fmt.Errorf("checksum manifest does not contain %s", filename)
	}
	return found, nil
}

func extractUpdateArchive(data []byte, dest, expectedExecutable string) (string, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open update archive: %w", err)
	}
	if len(r.File) == 0 || len(r.File) > maxArchiveFiles {
		return "", fmt.Errorf("update archive contains %d files, allowed range is 1-%d", len(r.File), maxArchiveFiles)
	}

	var executablePath string
	var extractedBytes uint64
	for _, file := range r.File {
		if file.Name == "" || strings.Contains(file.Name, "\\") {
			return "", fmt.Errorf("invalid update archive path %q", file.Name)
		}
		cleanName := filepath.ToSlash(filepath.Clean(file.Name))
		if cleanName != file.Name || !filepath.IsLocal(filepath.FromSlash(cleanName)) || strings.Contains(cleanName, "/") {
			return "", fmt.Errorf("update archive entry must be a top-level file: %q", file.Name)
		}
		info := file.FileInfo()
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("unsupported update archive entry %q", file.Name)
		}
		if file.UncompressedSize64 > uint64(maxExtractedBytes)-extractedBytes {
			return "", fmt.Errorf("uncompressed update archive exceeds %d bytes", maxExtractedBytes)
		}
		extractedBytes += file.UncompressedSize64

		target := filepath.Join(dest, cleanName)
		if err := extractFile(file, target); err != nil {
			return "", err
		}
		if cleanName == expectedExecutable {
			executablePath = target
		}
	}
	if executablePath == "" {
		return "", fmt.Errorf("update archive does not contain expected executable %s", expectedExecutable)
	}
	return executablePath, nil
}

func extractFile(file *zip.File, target string) error {
	rc, err := file.Open()
	if err != nil {
		return fmt.Errorf("open update archive entry %s: %w", file.Name, err)
	}
	defer rc.Close()

	mode := file.Mode().Perm()
	if mode == 0 {
		mode = 0755
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create extracted file %s: %w", file.Name, err)
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(target)
		}
	}()

	entryLimit := int64(file.UncompressedSize64) + 1
	if file.UncompressedSize64 >= uint64(^uint64(0)>>1) {
		entryLimit = int64(maxExtractedBytes) + 1
	}
	written, err := io.Copy(out, io.LimitReader(rc, entryLimit))
	if err != nil {
		return fmt.Errorf("extract update archive entry %s: %w", file.Name, err)
	}
	if written != int64(file.UncompressedSize64) {
		return fmt.Errorf("extracted size mismatch for %s", file.Name)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync extracted file %s: %w", file.Name, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close extracted file %s: %w", file.Name, err)
	}
	ok = true
	return nil
}
