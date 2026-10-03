// Command modulezip creates a Go module archive with the upstream module rules.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
	modzip "golang.org/x/mod/zip"
)

type archiveReport struct {
	Path          string            `json:"Path"`
	Version       string            `json:"Version"`
	Sum           string            `json:"Sum"`
	GoModSum      string            `json:"GoModSum"`
	ArchiveSHA256 string            `json:"ArchiveSHA256"`
	Requires      map[string]string `json:"Requires"`
}

func createArchive(source, path, version, output string) (_ archiveReport, err error) {
	if err := module.Check(path, version); err != nil {
		return archiveReport{}, err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return archiveReport{}, err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return archiveReport{}, err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return archiveReport{}, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return archiveReport{}, err
	}
	output = filepath.Join(parent, filepath.Base(output))
	relative, err := filepath.Rel(source, output)
	if err != nil || filepath.IsLocal(relative) {
		return archiveReport{}, errors.New("archive output must be outside the module source directory")
	}
	modPath := filepath.Join(source, "go.mod")
	info, err := os.Lstat(modPath)
	if err != nil {
		return archiveReport{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > modzip.MaxGoMod {
		return archiveReport{}, errors.New("go.mod must be a regular file within the Go module size limit")
	}
	data, err := os.ReadFile(modPath)
	if err != nil {
		return archiveReport{}, err
	}
	parsed, err := modfile.Parse(modPath, data, nil)
	if err != nil {
		return archiveReport{}, err
	}
	if parsed.Module == nil || parsed.Module.Mod.Path != path {
		return archiveReport{}, errors.New("module path does not match go.mod")
	}
	if len(parsed.Replace) != 0 || len(parsed.Exclude) != 0 {
		return archiveReport{}, errors.New("versioned installation forbids replace and exclude directives")
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return archiveReport{}, err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(output)
		}
	}()
	writeErr := modzip.CreateFromDir(file, module.Version{Path: path, Version: version}, source)
	closeErr := file.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		return archiveReport{}, err
	}
	contents, err := zip.OpenReader(output)
	if err != nil {
		return archiveReport{}, err
	}
	definition, err := contents.Open(path + "@" + version + "/go.mod")
	if err != nil {
		return archiveReport{}, errors.Join(err, contents.Close())
	}
	archived, modReadErr := io.ReadAll(io.LimitReader(definition, modzip.MaxGoMod+1))
	if err = errors.Join(modReadErr, definition.Close(), contents.Close()); err != nil {
		return archiveReport{}, err
	}
	if !bytes.Equal(archived, data) {
		return archiveReport{}, errors.New("go.mod changed during module archive creation")
	}
	sum, err := dirhash.HashZip(output, dirhash.Hash1)
	if err != nil {
		return archiveReport{}, err
	}
	modSum, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	})
	if err != nil {
		return archiveReport{}, err
	}
	archive, err := os.Open(output)
	if err != nil {
		return archiveReport{}, err
	}
	digest := sha256.New()
	_, readErr := io.Copy(digest, archive)
	err = errors.Join(readErr, archive.Close())
	if err != nil {
		return archiveReport{}, err
	}
	requires := make(map[string]string, len(parsed.Require))
	for _, requirement := range parsed.Require {
		requires[requirement.Mod.Path] = requirement.Mod.Version
	}
	return archiveReport{Path: path, Version: version, Sum: sum, GoModSum: modSum,
		ArchiveSHA256: hex.EncodeToString(digest.Sum(nil)), Requires: requires}, nil
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("modulezip", flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source", "", "committed module source directory")
	path := flags.String("module", "", "expected module path")
	version := flags.String("version", "", "canonical module version")
	output := flags.String("out", "", "new module ZIP outside the source directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *source == "" || *path == "" || *version == "" || *output == "" || flags.NArg() != 0 {
		return errors.New("source, module, version and out are required; positional arguments are unsupported")
	}
	report, err := createArchive(*source, *path, *version, *output)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(report)
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
