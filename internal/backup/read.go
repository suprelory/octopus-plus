package backup

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

type manifest struct {
	Version      int       `json:"version"`
	ExportedAt   time.Time `json:"exported_at"`
	IncludeLogs  bool      `json:"include_logs"`
	IncludeStats bool      `json:"include_stats"`
	Format       string    `json:"format"`
	Tables       []string  `json:"tables,omitempty"`
}

// Table names are the DTO's JSON names. This registry is shared by both archive
// readers and writers so adding a backup field cannot silently omit it from ZIP.
func tables(d *Document) map[string]any {
	result := map[string]any{}
	var collect func(reflect.Value)
	collect = func(value reflect.Value) {
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if field.Anonymous {
				collect(value.Field(i))
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.Type.Kind() == reflect.Slice && name != "" && name != "-" {
				result[name] = value.Field(i).Addr().Interface()
			}
		}
	}
	collect(reflect.ValueOf(d).Elem())
	return result
}

func relayTable(name string) bool {
	return name == "relay_logs" || name == "relay_request_contents" || name == "relay_attempt_contents"
}

// Read recognizes JSON (including the former API envelope) and ZIP. Relay
// history stays in the private ZIP spool and is decoded a row at a time during
// validation/import. Close must be called after using the document.
func Read(ctx context.Context, reader io.Reader) (*Document, error) {
	buffer := bufio.NewReader(contextReader{ctx, reader})
	magic, _ := buffer.Peek(4)
	if string(magic) == "PK\x03\x04" {
		return readZIP(ctx, buffer)
	}
	var envelope struct {
		Document
		Data json.RawMessage `json:"data"`
	}
	if err := decodeJSON(buffer, &envelope); err != nil {
		return nil, ErrFormat
	}
	if envelope.Version == 0 && len(envelope.Data) > 0 {
		if err := decodeJSON(strings.NewReader(string(envelope.Data)), &envelope.Document); err != nil {
			return nil, ErrFormat
		}
	}
	if envelope.Version != 1 && envelope.Version != Version {
		return nil, ErrFormat
	}
	return &envelope.Document, nil
}

func decodeJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(target); err != nil {
		return ErrFormat
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrFormat
	}
	return nil
}

func readZIP(ctx context.Context, reader io.Reader) (_ *Document, err error) {
	file, err := os.CreateTemp("", "octopus-restore-*.zip")
	if err != nil {
		return nil, ErrStorage
	}
	d := &Document{temporary: file, archive: map[string]*zip.File{}}
	defer func() {
		if err != nil {
			_ = d.Close()
		}
	}()
	size, err := io.Copy(file, contextReader{ctx, reader})
	if err != nil {
		return nil, ErrFormat
	}
	archive, err := zip.NewReader(file, size)
	if err != nil {
		return nil, ErrFormat
	}
	entries := map[string]*zip.File{}
	for _, entry := range archive.File {
		if entries[entry.Name] != nil || strings.ContainsAny(entry.Name, "/\\") {
			return nil, ErrFormat
		}
		entries[entry.Name] = entry
	}
	var header manifest
	if err := readZIPJSON(ctx, entries["manifest.json"], &header); err != nil {
		return nil, err
	}
	if header.Version != 1 && header.Version != Version || header.Version == 1 && header.Format != "zip-v1" || header.Version == Version && header.Format != "zip-v2" {
		return nil, ErrFormat
	}
	d.Version, d.ExportedAt, d.IncludeLogs, d.IncludeStats = header.Version, header.ExportedAt, header.IncludeLogs, header.IncludeStats
	targets := tables(d)
	for name, entry := range entries {
		if name == "manifest.json" {
			continue
		}
		key, ndjson := strings.TrimSuffix(name, ".json"), false
		if strings.HasSuffix(name, ".ndjson") {
			key, ndjson = strings.TrimSuffix(name, ".ndjson"), true
		}
		target, ok := targets[key]
		if !ok || d.archive[key] != nil {
			return nil, ErrFormat
		}
		d.archive[key] = entry
		if relayTable(key) {
			continue
		}
		if ndjson {
			if err := readNDJSON(ctx, entry, reflect.ValueOf(target).Elem()); err != nil {
				return nil, err
			}
		} else if err := readZIPJSON(ctx, entry, target); err != nil {
			return nil, err
		}
	}
	for _, name := range header.Tables {
		if d.archive[name] == nil {
			return nil, ErrFormat
		}
	}
	if header.Version == Version {
		expected := archiveTables(d)
		slices.Sort(header.Tables)
		if !slices.Equal(expected, header.Tables) || len(d.archive) != len(expected) {
			return nil, ErrFormat
		}
	}
	return d, nil
}

func archiveTables(d *Document) []string {
	names := []string{}
	for name := range tables(d) {
		if v2Table(name) && (d.IncludeLogs || !relayTable(name)) && (d.IncludeStats || !statisticsTable(name)) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func readZIPJSON(ctx context.Context, entry *zip.File, target any) error {
	if entry == nil {
		return ErrFormat
	}
	reader, err := entry.Open()
	if err != nil {
		return ErrFormat
	}
	defer reader.Close()
	return decodeJSON(contextReader{ctx, reader}, target)
}

func readNDJSON(ctx context.Context, entry *zip.File, target reflect.Value) error {
	reader, err := entry.Open()
	if err != nil {
		return ErrFormat
	}
	defer reader.Close()
	decoder := json.NewDecoder(contextReader{ctx, reader})
	for {
		row := reflect.New(target.Type().Elem())
		if err := decoder.Decode(row.Interface()); err != nil {
			if err == io.EOF {
				return nil
			}
			return ErrFormat
		}
		target.Set(reflect.Append(target, row.Elem()))
	}
}

func walkRows[T any](ctx context.Context, d *Document, name string, rows []T, visit func(T) error) error {
	entry := d.archive[name]
	if entry == nil {
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(row); err != nil {
				return err
			}
		}
		return nil
	}
	reader, err := entry.Open()
	if err != nil {
		return ErrFormat
	}
	defer reader.Close()
	decoder := json.NewDecoder(contextReader{ctx, reader})
	array := strings.HasSuffix(entry.Name, ".json")
	if array {
		token, err := decoder.Token()
		if err != nil {
			return ErrFormat
		}
		if token == nil {
			if _, err := decoder.Token(); err == io.EOF {
				return nil
			}
			return ErrFormat
		}
		if token != json.Delim('[') {
			return ErrFormat
		}
	}
	for !array || decoder.More() {
		var row T
		if err := decoder.Decode(&row); err != nil {
			if err == io.EOF && !array {
				return nil
			}
			return ErrFormat
		}
		if err := visit(row); err != nil {
			return err
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
		return ErrFormat
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrFormat
	}
	return nil
}

func walkRelay(ctx context.Context, d *Document, visit func(string, any) error) error {
	if err := walkRows(ctx, d, "relay_logs", d.RelayLogs, func(row model.RelayLog) error { return visit("relay_logs", row) }); err != nil {
		return err
	}
	if err := walkRows(ctx, d, "relay_request_contents", d.RelayRequestContents, func(row model.RelayRequestContent) error { return visit("relay_request_contents", row) }); err != nil {
		return err
	}
	return walkRows(ctx, d, "relay_attempt_contents", d.RelayAttemptContents, func(row model.RelayAttemptContent) error { return visit("relay_attempt_contents", row) })
}

// LegacyDump materializes streamed relay rows for the v1 incremental importer.
func (d *Document) LegacyDump(ctx context.Context) (*model.LegacyDBDump, error) {
	if d.Version != 1 {
		return nil, ErrFormat
	}
	copy := d.LegacyDBDump
	if d.archive == nil {
		return &copy, nil
	}
	copy.RelayLogs, copy.RelayRequestContents, copy.RelayAttemptContents = nil, nil, nil
	err := walkRelay(ctx, d, func(_ string, row any) error {
		switch value := row.(type) {
		case model.RelayLog:
			copy.RelayLogs = append(copy.RelayLogs, value)
		case model.RelayRequestContent:
			copy.RelayRequestContents = append(copy.RelayRequestContents, value)
		case model.RelayAttemptContent:
			copy.RelayAttemptContents = append(copy.RelayAttemptContents, value)
		}
		return nil
	})
	return &copy, err
}
