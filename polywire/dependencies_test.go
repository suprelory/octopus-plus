package polywire_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const transformerImportPrefix = "github.com/suprelory/octopus-plus/polywire/"

func TestTransformerDependencyDirection(t *testing.T) {
	t.Helper()
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(path)
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			assertAllowedTransformerImport(t, rel, importPath, fileSet.Position(spec.Pos()).Line)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertAllowedTransformerImport(t *testing.T, source, importPath string, line int) {
	t.Helper()
	fail := func(reason string) {
		t.Errorf("%s imports %q: %s (line %d)", source, importPath, reason, line)
	}
	host := "github.com/suprelory/octopus-plus"
	if (importPath == host || strings.HasPrefix(importPath, host+"/")) &&
		importPath != strings.TrimSuffix(transformerImportPrefix, "/") && !strings.HasPrefix(importPath, transformerImportPrefix) {
		fail("Polywire, including its tests, must not depend on the host application")
	}
	if strings.HasSuffix(source, "_test.go") {
		return // Contract tests intentionally exercise inbound/outbound pairs.
	}

	if strings.HasPrefix(source, "protocol/") && strings.HasPrefix(importPath, transformerImportPrefix) {
		fail("protocol wire packages must not depend on canonical models or adapters")
	}
	if (strings.HasPrefix(source, "model/") || strings.HasPrefix(source, "rawjson/")) &&
		strings.HasPrefix(importPath, transformerImportPrefix) {
		fail("canonical model and raw JSON packages must not depend on adapters or host services")
	}
	if strings.HasPrefix(source, "inbound/") && strings.HasPrefix(importPath, transformerImportPrefix+"outbound/") {
		fail("inbound adapters must not depend on outbound adapters")
	}
	if strings.HasPrefix(source, "outbound/") && strings.HasPrefix(importPath, transformerImportPrefix+"inbound/") {
		fail("outbound adapters must not depend on inbound adapters; use protocol DTOs")
	}
}
