package safego

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sleepBriefly() { time.Sleep(5 * time.Millisecond) }

// unprotectedGoStatements returns the positions of `go` statements in src that are not a func
// literal whose first statement is `defer safego.Recover(...)`.
func unprotectedGoStatements(fset *token.FileSet, file *ast.File) []string {
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		stmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		if !isProtectedClosure(stmt.Call) {
			found = append(found, fset.Position(stmt.Pos()).String())
		}
		return true
	})
	return found
}

func isProtectedClosure(call *ast.CallExpr) bool {
	lit, ok := call.Fun.(*ast.FuncLit)
	if !ok || len(lit.Body.List) == 0 {
		return false
	}
	deferStmt, ok := lit.Body.List[0].(*ast.DeferStmt)
	if !ok {
		return false
	}
	sel, ok := deferStmt.Call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Recover" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "safego"
}

func TestEveryBackgroundGoroutineIsProtected(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var violations []string

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "node_modules", "vendor", ".git":
				return filepath.SkipDir
			}
			if filepath.Base(filepath.Dir(path)) == "pkg" && d.Name() == "safego" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		violations = append(violations, unprotectedGoStatements(fset, file)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("go statements without safego protection (use safego.Go, or start the closure with `defer safego.Recover(name)`):\n%s", strings.Join(violations, "\n"))
	}
}

func TestTheWalkerFlagsAnUnprotectedGoStatement(t *testing.T) {
	src := `package x
func f() {
	go func() { println("bare") }()
	go g()
	go func() { defer safego.Recover("ok"); println("fine") }()
}
func g() {}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := unprotectedGoStatements(fset, file); len(got) != 2 {
		t.Fatalf("flagged %d statements %v, want 2 (the bare closure and the named call)", len(got), got)
	}
}
