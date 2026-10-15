// Package analyzer contains individual analysis passes for gecko's
// platform API conventions. Each analyzer is a standalone
// golang.org/x/tools/go/analysis.Analyzer that can be composed into a
// golangci-lint plugin.
package analyzer

import (
	"go/ast"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// RequiredNoOmitempty reports struct fields that carry both a +required marker
// and an omitempty tag in the JSON struct tag. The platform API conventions
// state that +required MUST NOT have omitempty because omitempty suppresses
// zero-valued required fields from serialisation, making them invisible to
// clients that rely on the field being present.
var RequiredNoOmitempty = &analysis.Analyzer{
	Name:     "requirednoomitempty",
	Doc:      "reports +required fields that also carry omitempty in their JSON tag",
	Run:      runRequiredNoOmitempty,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

func runRequiredNoOmitempty(pass *analysis.Pass) (any, error) {
	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	ins.Preorder([]ast.Node{(*ast.StructType)(nil)}, func(n ast.Node) {
		st := n.(*ast.StructType)
		if st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			if fieldHasExactMarker(field, "+required") && fieldTagHasOmitempty(field) {
				pass.Reportf(field.Pos(),
					"+required field must not have omitempty in its JSON tag")
			}
		}
	})
	return nil, nil //nolint:nilnil
}
