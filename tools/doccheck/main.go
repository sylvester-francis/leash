// Copyright 2026 Sylvester Francis
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command doccheck fails when any exported top-level symbol or exported method
// lacks a doc comment. It is a std-lib AST walker with no dependencies, wired
// into `make doc-check` and CI to keep godoc coverage complete. Test files are
// skipped: their exported helpers are not part of any package's API.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	fset := token.NewFileSet()
	var problems []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "vendor" || (strings.HasPrefix(name, ".") && name != ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			problems = append(problems, fmt.Sprintf("%s: parse: %v", path, perr))
			return nil
		}
		problems = append(problems, checkFile(fset, file)...)
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "doc-check: walk: %v\n", err)
		os.Exit(2)
	}

	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, p)
		}
		fmt.Fprintf(os.Stderr, "doc-check: %d undocumented exported symbol(s)\n", len(problems))
		os.Exit(1)
	}
	fmt.Println("doc-check: ok")
}

// checkFile returns a problem line for each undocumented exported symbol.
func checkFile(fset *token.FileSet, file *ast.File) []string {
	var out []string
	report := func(pos token.Pos, what string) {
		out = append(out, fmt.Sprintf("%s: exported %s is undocumented", fset.Position(pos), what))
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() || !exportedReceiver(d.Recv) {
				continue
			}
			if !documented(d.Doc) {
				report(d.Pos(), symbolKind(d)+" "+d.Name.Name)
			}
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			checkGenDecl(d, report)
		}
	}
	return out
}

// checkGenDecl checks the exported type, const, and var specs in a declaration.
// A group doc comment documents every spec in the group.
func checkGenDecl(d *ast.GenDecl, report func(token.Pos, string)) {
	groupDoc := documented(d.Doc)
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if s.Name.IsExported() && !groupDoc && !documented(s.Doc) {
				report(s.Pos(), "type "+s.Name.Name)
			}
		case *ast.ValueSpec:
			for _, name := range s.Names {
				if name.IsExported() && !groupDoc && !documented(s.Doc) {
					report(name.Pos(), "value "+name.Name)
				}
			}
		}
	}
}

// documented reports whether a doc comment is present and non-empty.
func documented(doc *ast.CommentGroup) bool {
	return doc != nil && strings.TrimSpace(doc.Text()) != ""
}

// exportedReceiver reports whether a func's receiver (if any) is an exported
// type. A method on an unexported type is not part of the package API.
func exportedReceiver(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) == 0 {
		return true // a plain function, not a method
	}
	t := recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.IsExported()
	}
	return true
}

// symbolKind names a func declaration as a func or a method.
func symbolKind(d *ast.FuncDecl) string {
	if d.Recv != nil {
		return "method"
	}
	return "func"
}
