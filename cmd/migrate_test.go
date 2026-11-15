package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v2 "github.com/suprelory/octopus-plus/internal/migration/v2"
)

func TestV2PreflightCommand(t *testing.T) {
	for _, test := range []struct {
		name, backup string
		blocked      bool
	}{
		{"empty", `{"version":1}`, false},
		{"broken", `{"version":1,"site_accounts":[{"id":1,"site_id":999,"password":"private-token"}]}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "backup.json")
			if err := os.WriteFile(path, []byte(test.backup), 0600); err != nil {
				t.Fatal(err)
			}
			command := newMigrationCommand()
			var output, diagnostic bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&diagnostic)
			command.SetArgs([]string{"v2-preflight", "--input", path})
			err := command.Execute()
			if (err != nil) != test.blocked {
				t.Fatalf("command error = %v", err)
			}
			var report v2.Report
			if err := json.Unmarshal(output.Bytes(), &report); err != nil {
				t.Fatalf("invalid report: %v", err)
			}
			if (report.Summary.Blockers > 0) != test.blocked {
				t.Fatalf("wrong blocker status: %+v", report.Summary)
			}
			if strings.Contains(output.String()+diagnostic.String(), "private-token") {
				t.Fatal("command leaked credentials")
			}
			unchanged, err := os.ReadFile(path)
			if err != nil || string(unchanged) != test.backup {
				t.Fatal("command modified backup")
			}
		})
	}
}
