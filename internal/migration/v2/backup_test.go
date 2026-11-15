package v2

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func backupFields(t *testing.T, snapshot *Snapshot) map[string]json.RawMessage {
	t.Helper()
	result := make(map[string]json.RawMessage)
	for key, value := range snapshot.fields() {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		result[key] = encoded
	}
	return result
}

func TestJSONAndZIPProduceSameReport(t *testing.T) {
	snapshot := fixture()
	fields := backupFields(t, snapshot)
	fields["relay_logs"] = json.RawMessage(`[{"response":"history-secret","nested":[{"a":1}]}]`)
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	jsonSnapshot, err := readJSONBackup(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for name, data := range fields {
		switch name {
		case "version", "exported_at", "include_logs", "include_stats":
			continue
		}
		writer, err := archive.Create(name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	writer, err := archive.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte(`{"version":1,"format":"zip-v1","include_logs":true,"include_stats":true}`))
	// This path is deliberately never extracted or decoded.
	writer, _ = archive.Create("../untrusted.json")
	_, _ = writer.Write([]byte("not JSON"))
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(t.TempDir(), "backup.zip")
	if err := os.WriteFile(zipPath, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	zipSnapshot, err := ReadBackup(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Analyze(jsonSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Analyze(zipSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("format changed report:\n%+v\n%+v", a, b)
	}
}

func TestInvalidBackupsDoNotExposeInput(t *testing.T) {
	for _, body := range []string{
		`{"version":1,"site_accounts":[{"id":"private-token"}]}`,
		`{"version":1,"version":1,"private-token":true}`,
		`{"version":1} {"private-token":true}`,
		`{"version":2,"private-token":true}`,
		`{"version":1,"extra":[{"x":"private-token"}`,
	} {
		_, err := readJSONBackup(strings.NewReader(body))
		if err == nil || strings.Contains(err.Error(), "private-token") {
			t.Fatalf("unsafe or missing validation error: %v", err)
		}
	}
}

func TestZIPRequiresConfigurationTables(t *testing.T) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	writer, _ := archive.Create("manifest.json")
	_, _ = writer.Write([]byte(`{"version":1,"format":"zip-v1"}`))
	_ = archive.Close()
	if _, err := readZIPBackup(bytes.NewReader(buffer.Bytes()), int64(buffer.Len())); err == nil {
		t.Fatal("incomplete ZIP accepted")
	}
}
