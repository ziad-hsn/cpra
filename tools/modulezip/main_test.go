package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

func put(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveUsesModuleBoundariesAndPreservesUnusualExtensions(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	const path = "example.com/module"
	const version = "v0.1.0-rc.1"
	for name, content := range map[string]string{
		"go.mod": "module " + path + "\n\ngo 1.25.0\n", "LICENSE": "fixture license",
		"payload.bin": "required embedded bytes", ".github/workflows/verify.yml": "fixture workflow",
		"source.go": "package module\n", "nested/go.mod": "module example.com/nested\n",
		"nested/private.go": "package nested\n", "vendor/example.com/x/x.go": "package x\n",
		".git/config": "fixture VCS metadata",
	} {
		put(t, source, name, content)
	}
	output := filepath.Join(parent, "module.zip")
	report, err := createArchive(source, path, version, output)
	if err != nil {
		t.Fatal(err)
	}
	if report.Path != path || report.Version != version || report.Sum == "" || report.GoModSum == "" {
		t.Fatalf("missing archive identity: %+v", report)
	}
	if _, err := modzip.CheckZip(module.Version{Path: path, Version: version}, output); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var names []string
	for _, file := range archive.File {
		names = append(names, file.Name)
	}
	prefix := path + "@" + version + "/"
	expected := []string{prefix + ".github/workflows/verify.yml", prefix + "LICENSE", prefix + "go.mod", prefix + "payload.bin", prefix + "source.go"}
	if !reflect.DeepEqual(names, expected) {
		t.Fatalf("archive names: got %v, want %v", names, expected)
	}
	second, err := createArchive(source, path, version, filepath.Join(parent, "second.zip"))
	if err != nil || !reflect.DeepEqual(report, second) {
		t.Fatalf("archive generation changed: %+v %v", second, err)
	}
	if _, err := createArchive(source, path, version, output); err == nil {
		t.Fatal("overwrote an existing archive")
	}
}

func TestArchiveRejectsInvalidInputsWithoutLeavingAnArchive(t *testing.T) {
	for _, change := range []string{"module", "replace", "exclude", "collision", "nested-output"} {
		t.Run(change, func(t *testing.T) {
			parent := t.TempDir()
			source := filepath.Join(parent, "source")
			text := "module example.com/module\n\ngo 1.25.0\n"
			switch change {
			case "module":
				text = "module example.com/other\n"
			case "replace":
				text += "replace example.com/other => ../other\n"
			case "exclude":
				text += "exclude example.com/other v1.0.0\n"
			case "collision":
				put(t, source, "Name", "fixture")
				if _, err := os.Stat(filepath.Join(source, "name")); err == nil {
					t.Skip("creating a case-collision fixture requires a case-sensitive filesystem")
				}
				put(t, source, "name", "fixture")
			}
			put(t, source, "go.mod", text)
			output := filepath.Join(parent, "module.zip")
			if change == "nested-output" {
				output = filepath.Join(source, "module.zip")
			}
			if _, err := createArchive(source, "example.com/module", "v0.1.0", output); err == nil {
				t.Fatal("invalid input was accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("failed operation left archive: %v", err)
			}
		})
	}
}

func TestCommandReportsPinnedRequirements(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	put(t, source, "go.mod", "module example.com/worker\n\ngo 1.25.0\n\nrequire example.com/sdk v0.2.0\n")
	var stdout, stderr bytes.Buffer
	err := run([]string{"-source", source, "-module", "example.com/worker", "-version", "v0.3.0",
		"-out", filepath.Join(parent, "module.zip")}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	var report archiveReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Requires["example.com/sdk"] != "v0.2.0" {
		t.Fatalf("dependency identity missing: %+v", report)
	}
}
