package v2

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// Limit configuration input, not compressed archive size. ZIP request/history
// entries are never decompressed. JSON history is streamed past without retention.
const maxConfigurationBytes = 128 << 20

// ReadBackup accepts the application's v1 JSON and zip-v1 exports. It never
// extracts files or passes raw decode errors (which can contain secrets) onward.
func ReadBackup(path string) (*Snapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open input backup")
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return nil, errors.New("input backup must be a regular file")
	}
	var magic [4]byte
	_, _ = file.ReadAt(magic[:], 0)
	if magic[0] == 'P' && magic[1] == 'K' {
		return readZIPBackup(file, stat.Size())
	}
	if stat.Size() > maxConfigurationBytes {
		return nil, errors.New("JSON backup exceeds 128 MiB; use a ZIP backup for large histories")
	}
	return readJSONBackup(file)
}

func readJSONBackup(reader io.Reader) (*Snapshot, error) {
	snapshot := &Snapshot{}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("backup must be a JSON object")
	}
	fields := snapshot.fields()
	seen := make(map[string]bool)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid JSON backup")
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return nil, errors.New("invalid or duplicate backup field")
		}
		seen[name] = true
		if target, exists := fields[name]; exists {
			if err := decoder.Decode(target); err != nil {
				// name is from a fixed allowlist, never supplied as diagnostic data.
				return nil, fmt.Errorf("invalid backup field: %s", name)
			}
		} else if err := skipJSONValue(decoder); err != nil {
			return nil, errors.New("invalid JSON backup")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("incomplete JSON backup")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after JSON backup")
	}
	if snapshot.Version != 1 {
		return nil, errors.New("preflight requires a version 1 backup")
	}
	return snapshot, nil
}

func skipJSONValue(decoder *json.Decoder) error {
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := first.(json.Delim)
	if !ok || (delim != '{' && delim != '[') {
		return nil
	}
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

func readZIPBackup(reader io.ReaderAt, size int64) (*Snapshot, error) {
	archive, err := zip.NewReader(reader, size)
	if err != nil {
		return nil, errors.New("invalid ZIP backup")
	}
	snapshot := &Snapshot{}
	fields := snapshot.fields()
	var manifest struct {
		Version      int    `json:"version"`
		Format       string `json:"format"`
		ExportedAt   string `json:"exported_at"`
		IncludeLogs  bool   `json:"include_logs"`
		IncludeStats bool   `json:"include_stats"`
	}
	seen := make(map[string]bool)
	var total uint64
	for _, entry := range archive.File {
		name := entry.Name
		var target any
		if name == "manifest.json" {
			target = &manifest
		} else if len(name) > 5 && name[len(name)-5:] == ".json" {
			key := name[:len(name)-5]
			// Scalar fields belong exclusively to the manifest.
			switch key {
			case "version", "exported_at", "include_logs", "include_stats":
				continue
			}
			target = fields[key]
		}
		if target == nil {
			continue
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate ZIP configuration entry: %s", name)
		}
		seen[name] = true
		if entry.UncompressedSize64 > maxConfigurationBytes-total {
			return nil, errors.New("ZIP configuration exceeds 128 MiB")
		}
		total += entry.UncompressedSize64
		if err := decodeZIPEntry(entry, target); err != nil {
			return nil, fmt.Errorf("invalid ZIP configuration entry: %s", name)
		}
	}
	for _, required := range []string{"manifest.json", "channels.json", "channel_keys.json", "sites.json", "site_accounts.json", "site_channel_bindings.json", "groups.json", "group_items.json", "settings.json"} {
		if !seen[required] {
			return nil, fmt.Errorf("missing ZIP configuration entry: %s", required)
		}
	}
	if manifest.Version != 1 || manifest.Format != "zip-v1" {
		return nil, errors.New("preflight requires a version 1 zip-v1 backup")
	}
	snapshot.Version, snapshot.IncludeLogs, snapshot.IncludeStats = manifest.Version, manifest.IncludeLogs, manifest.IncludeStats
	if manifest.ExportedAt != "" {
		if err := snapshot.ExportedAt.UnmarshalText([]byte(manifest.ExportedAt)); err != nil {
			return nil, errors.New("invalid backup export timestamp")
		}
	}
	return snapshot, nil
}

func decodeZIPEntry(entry *zip.File, target any) error {
	reader, err := entry.Open()
	if err != nil {
		return err
	}
	defer reader.Close()
	decoder := json.NewDecoder(io.LimitReader(reader, maxConfigurationBytes+1))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing ZIP configuration data")
	}
	return nil
}
