package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestDatabaseFixtureURLsArePortable(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range files {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		positions := token.NewFileSet()
		source, err := parser.ParseFile(positions, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(source, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true
			}
			if strings.HasPrefix(value, "postgres"+"://") || strings.HasPrefix(value, "postgresql"+"://") {
				t.Errorf("%s: hardcoded PostgreSQL fixture connection bypasses TEST_DATABASE_URL", positions.Position(literal.Pos()))
			}
			return true
		})
	}
}
