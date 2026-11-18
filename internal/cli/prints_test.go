package cli_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheCommandPrintsThroughTerminal: no line of the command is printed
// with fmt, which writes a device's text as it came. Every one goes
// through internal/terminal, whatever it holds today: an event's value
// and a network's name are a stranger's to write, and no test of what is
// printed could name every line there will be.
func TestTheCommandPrintsThroughTerminal(t *testing.T) {
	t.Parallel()

	files := token.NewFileSet()

	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	read := 0

	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		source, parseErr := parser.ParseFile(files, name, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			t.Fatal(parseErr)
		}

		read++

		ast.Inspect(source, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}

			selector, isSelector := call.Fun.(*ast.SelectorExpr)
			if !isSelector {
				return true
			}

			from, isName := selector.X.(*ast.Ident)
			if !isName || from.Name != "fmt" {
				return true
			}

			if strings.HasPrefix(selector.Sel.Name, "Fprint") || strings.HasPrefix(selector.Sel.Name, "Print") {
				t.Errorf("%s: fmt.%s prints a device's text as it came; print with internal/terminal",
					files.Position(call.Pos()), selector.Sel.Name)
			}

			return true
		})
	}

	if read == 0 {
		t.Fatal("the command's source was not found beside its tests")
	}
}
