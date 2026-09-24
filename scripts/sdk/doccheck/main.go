// Command doccheck renders a package overview from go list's selected files.
// It respects the caller's build-tag selection without adding package stubs.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

type selection struct {
	Dir        string
	ImportPath string
	Name       string
	GoFiles    []string
}

func overview(selected selection) (string, error) {
	fset := token.NewFileSet()
	var files []*ast.File
	comments := 0
	for _, name := range selected.GoFiles {
		file, err := parser.ParseFile(fset, filepath.Join(selected.Dir, name), nil, parser.ParseComments)
		if err != nil {
			return "", err
		}
		files = append(files, file)
		if file.Doc != nil {
			comments++
		}
	}
	if comments != 1 {
		return "", fmt.Errorf("%s: want one package overview, got %d", selected.ImportPath, comments)
	}
	pkg, err := doc.NewFromFiles(fset, files, selected.ImportPath)
	if err != nil {
		return "", err
	}
	if pkg.Name != selected.Name || !strings.HasPrefix(pkg.Doc, "Package "+pkg.Name+" ") {
		return "", fmt.Errorf("%s: overview must begin with Package %s", selected.ImportPath, selected.Name)
	}
	return string(pkg.Text(pkg.Doc)), nil
}

func main() {
	var selected selection
	err := json.NewDecoder(os.Stdin).Decode(&selected)
	var rendered string
	if err == nil {
		rendered, err = overview(selected)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(rendered)
}
