package analyzer

import (
	"go/ast"
	"go/token"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// NoImmutableMarker reports any use of the +immutable marker. The platform
// API conventions forbid +immutable because it has no defined semantics in
// Kubebuilder. Immutability must be expressed with a CEL XValidation rule
// (e.g. +kubebuilder:validation:XValidation:rule="self == oldSelf").
var NoImmutableMarker = &analysis.Analyzer{
	Name:     "noimmutablemarker",
	Doc:      `reports use of the +immutable marker; use +kubebuilder:validation:XValidation:rule="self == oldSelf" instead`,
	Run:      runNoImmutableMarker,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

const immutableMsg = `+immutable has no defined semantics in Kubebuilder; ` +
	`use +kubebuilder:validation:XValidation:rule="self == oldSelf",message="<field> is immutable" instead`

func runNoImmutableMarker(pass *analysis.Pass) (any, error) {
	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	// Check field-level doc and trailing comments.
	ins.Preorder([]ast.Node{(*ast.StructType)(nil)}, func(n ast.Node) {
		st := n.(*ast.StructType)
		if st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			if fieldHasExactMarker(field, "+immutable") {
				pass.Reportf(field.Pos(), immutableMsg)
			}
		}
	})

	// Check type-level and package-level doc comments.
	// GenDecl.Doc holds the leading doc comment for type declarations;
	// File.Doc holds comments above the package clause.
	for _, f := range pass.Files {
		reportImmutableInCG(pass, f.Doc, f.Pos())
		ast.Inspect(f, func(n ast.Node) bool {
			gd, ok := n.(*ast.GenDecl)
			if ok {
				reportImmutableInCG(pass, gd.Doc, gd.Pos())
			}
			return true
		})
	}

	return nil, nil //nolint:nilnil
}

func reportImmutableInCG(pass *analysis.Pass, cg *ast.CommentGroup, pos token.Pos) {
	if cgHasExactMarker(cg, "+immutable") {
		pass.Reportf(pos, immutableMsg)
	}
}
