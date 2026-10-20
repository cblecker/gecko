package analyzer

import (
	"go/ast"
	"go/types"
	"strings"
	"unicode"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// NoIsPrefixBool reports bool-typed struct fields whose Go name starts with
// "Is". The platform API conventions state that boolean fields must never be
// named IsFoo; use Foo instead. The "Is" prefix adds no semantic value and
// diverges from the Kubernetes naming convention.
var NoIsPrefixBool = &analysis.Analyzer{
	Name:     "noisprefixbool",
	Doc:      `reports bool fields whose name starts with "Is" (e.g. IsEnabled → Enabled)`,
	Run:      runNoIsPrefixBool,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

func runNoIsPrefixBool(pass *analysis.Pass) (any, error) {
	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	nodeFilter := []ast.Node{(*ast.StructType)(nil)}
	ins.Preorder(nodeFilter, func(n ast.Node) {
		st, ok := n.(*ast.StructType)
		if !ok || st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			if !isBoolField(pass, field) {
				continue
			}
			for _, name := range field.Names {
				if strings.HasPrefix(name.Name, "Is") && len(name.Name) > 2 && unicode.IsUpper(rune(name.Name[2])) {
					suggested := name.Name[2:]
					pass.Reportf(name.Pos(),
						"bool field %q must not start with \"Is\"; use %q instead",
						name.Name, suggested)
				}
			}
		}
	})
	return nil, nil //nolint:nilnil
}

// isBoolField reports whether the field's type resolves to bool or *bool.
func isBoolField(pass *analysis.Pass, field *ast.Field) bool {
	t := pass.TypesInfo.TypeOf(field.Type)
	if t == nil {
		return false
	}
	// Unwrap pointer.
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind() == types.Bool
}
