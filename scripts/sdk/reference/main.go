// Command reference extracts exported Go declarations without compiling the SDK.
// It is a documentation tool, outside the published module dependency graph.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type entry struct {
	Name      string `json:"name"`
	Package   string `json:"package"`
	Tag       string `json:"tag"`
	Signature string `json:"signature"`
	Doc       string `json:"doc"`
}

func receiverExported(expr ast.Expr) bool {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.IsExported()
	case *ast.StarExpr:
		return receiverExported(value.X)
	case *ast.IndexExpr:
		return receiverExported(value.X)
	case *ast.IndexListExpr:
		return receiverExported(value.X)
	default:
		return false
	}
}

func declarations(path, pkg string) ([]entry, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	baseOnly := bytes.Contains(data, []byte("//go:build !externaljobs"))
	tag := ""
	if bytes.Contains(data, []byte("//go:build externaljobs")) {
		tag = "externaljobs"
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, data, parser.ParseComments)
	if err != nil {
		return nil, false, err
	}
	ast.FileExports(file)
	var entries []entry
	for _, decl := range file.Decls {
		doc, name := "", ""
		var node ast.Node
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() || (d.Recv != nil && !receiverExported(d.Recv.List[0].Type)) {
				continue
			}
			name = d.Name.Name
			if d.Recv != nil {
				var b bytes.Buffer
				if err := format.Node(&b, fset, d.Recv.List[0].Type); err != nil {
					return nil, false, err
				}
				name = b.String() + "." + name
			}
			if d.Doc != nil {
				doc = d.Doc.Text()
			}
			d.Body, d.Doc = nil, nil
			node = d
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			if d.Doc != nil {
				doc = d.Doc.Text()
			}
			d.Doc = nil
			node = d
			for _, sp := range d.Specs {
				switch s := sp.(type) {
				case *ast.TypeSpec:
					name += s.Name.Name + " "
				case *ast.ValueSpec:
					for _, n := range s.Names {
						name += n.Name + " "
					}
				}
			}
		default:
			continue
		}
		var b bytes.Buffer
		if err := format.Node(&b, fset, node); err != nil {
			return nil, false, err
		}
		entries = append(entries, entry{strings.TrimSpace(name), pkg, tag, b.String(), doc})
	}
	return entries, baseOnly, nil
}

func collect(root string) ([]entry, error) {
	entries := []entry{}
	for _, pkg := range []string{"", "api", "collection", "collection/commitment", "worker"} {
		paths, err := filepath.Glob(filepath.Join(root, "sdk/go", pkg, "*.go"))
		if err != nil {
			return nil, err
		}
		var selected []entry
		baseNames := make(map[string]bool)
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			decls, baseOnly, err := declarations(path, pkg)
			if err != nil {
				return nil, err
			}
			for _, e := range decls {
				if e.Tag == "" {
					baseNames[e.Name] = true
				}
				if !baseOnly {
					selected = append(selected, e)
				}
			}
		}
		for _, e := range selected {
			// The combined API projection contains shared and optional types.
			// Only symbols absent from the base projection require the tag.
			if pkg == "api" && baseNames[e.Name] {
				e.Tag = ""
			}
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		return a.Package+"/"+a.Name+"/"+a.Signature < b.Package+"/"+b.Name+"/"+b.Signature
	})
	return entries, nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: reference <repository-root>")
		os.Exit(2)
	}
	entries, err := collect(os.Args[1])
	if err == nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(entries)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
