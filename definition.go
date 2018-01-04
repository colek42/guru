// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package guru

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	pathpkg "path"
	"path/filepath"
	"strconv"

	"golang.org/x/tools/cmd/guru/serial"
	"golang.org/x/tools/go/buildutil"
	"golang.org/x/tools/go/loader"
)

type Response struct {
	Obj      types.Object
	Position token.Position
}

// definition reports the location of the definition of an identifier.
func definition(q *Query) (*Response, error) {
	// Run the type checker.
	lconf := loader.Config{Build: q.Build}
	allowErrors(&lconf)

	if _, err := importQueryPackage(q.Pos, &lconf); err != nil {
		return nil, err
	}

	// Load/parse/type-check the program.
	lprog, err := lconf.Load()
	if err != nil {
		return nil, err
	}

	qpos, err := parseQueryPos(lprog, q.Pos, false)
	if err != nil {
		return nil, err
	}

	id, _ := qpos.path[0].(*ast.Ident)
	if id == nil {
		return nil, fmt.Errorf("no identifier here")
	}

	// Look up the declaration of this identifier.
	// If id is an anonymous field declaration,
	// it is both a use of a type and a def of a field;
	// prefer the use in that case.
	obj := qpos.info.Uses[id]
	if obj == nil {
		obj = qpos.info.Defs[id]
		if obj == nil {
			// Happens for y in "switch y := x.(type)",
			// and the package declaration,
			// but I think that's all.
			return nil, fmt.Errorf("no object for identifier")
		}
	}

	if !obj.Pos().IsValid() {
		return nil, fmt.Errorf("%s is built in", obj.Name())
	}

	position := qpos.fset.Position(obj.Pos())

	response := &Response{
		Obj:      obj,
		Position: position,
	}
	return response, nil
}

// packageForQualIdent returns the package p if id is X in a qualified
// identifier p.X; it returns "" otherwise.
//
// Precondition: id is path[0], and the parser did not resolve id to a
// local object.  For speed, packageForQualIdent assumes that p is a
// package iff it is the basename of an import path (and not, say, a
// package-level decl in another file or a predeclared identifier).
func packageForQualIdent(path []ast.Node, id *ast.Ident) string {
	if sel, ok := path[1].(*ast.SelectorExpr); ok && sel.Sel == id && ast.IsExported(id.Name) {
		if pkgid, ok := sel.X.(*ast.Ident); ok && pkgid.Obj == nil {
			f := path[len(path)-1].(*ast.File)
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				if imp.Name != nil {
					if imp.Name.Name == pkgid.Name {
						return path // renaming import
					}
				} else if pathpkg.Base(path) == pkgid.Name {
					return path // ordinary import
				}
			}
		}
	}
	return ""
}

// findPackageMember returns the type and position of the declaration of
// pkg.member by loading and parsing the files of that package.
// srcdir is the directory in which the import appears.
func findPackageMember(ctxt *build.Context, fset *token.FileSet, srcdir, pkg, member string) (token.Token, token.Pos, error) {
	bp, err := ctxt.Import(pkg, srcdir, 0)
	if err != nil {
		return 0, token.NoPos, err // no files for package
	}

	// TODO(adonovan): opt: parallelize.
	for _, fname := range bp.GoFiles {
		filename := filepath.Join(bp.Dir, fname)

		// Parse the file, opening it the file via the build.Context
		// so that we observe the effects of the -modified flag.
		f, _ := buildutil.ParseFile(fset, ctxt, nil, ".", filename, parser.Mode(0))
		if f == nil {
			continue
		}

		// Find a package-level decl called 'member'.
		for _, decl := range f.Decls {
			switch decl := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					switch spec := spec.(type) {
					case *ast.ValueSpec:
						// const or var
						for _, id := range spec.Names {
							if id.Name == member {
								return decl.Tok, id.Pos(), nil
							}
						}
					case *ast.TypeSpec:
						if spec.Name.Name == member {
							return token.TYPE, spec.Name.Pos(), nil
						}
					}
				}
			case *ast.FuncDecl:
				if decl.Recv == nil && decl.Name.Name == member {
					return token.FUNC, decl.Name.Pos(), nil
				}
			}
		}
	}

	return 0, token.NoPos, fmt.Errorf("couldn't find declaration of %s in %q", member, pkg)
}

type definitionResult struct {
	pos   token.Pos // (nonzero) location of definition
	descr string    // description of object it denotes
}

func (r *definitionResult) PrintPlain(printf printfFunc) {
	printf(r.pos, "defined here as %s", r.descr)
}

func (r *definitionResult) JSON(fset *token.FileSet) []byte {
	return toJSON(&serial.Definition{
		Desc:   r.descr,
		ObjPos: fset.Position(r.pos).String(),
	})
}
