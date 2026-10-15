package analyzer

import (
	"go/ast"
	"go/token"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// PreferStandardMarkers reports use of kubebuilder-specific marker variants
// when a toolchain-agnostic standard marker exists. The platform API
// conventions require the standard form so that generated output is compatible
// with a wider range of tools.
//
// Reported substitutions:
//
//	+kubebuilder:default=<v>          → +default=<v>
//	+kubebuilder:default:=<v>         → +default=<v>  (literal-argument form)
//	+kubebuilder:validation:Optional  → +optional
//	+kubebuilder:validation:Required  → +required
//	+kubebuilder:validation:ListType  → +listType
var PreferStandardMarkers = &analysis.Analyzer{
	Name:     "preferstandardmarkers",
	Doc:      "reports kubebuilder-specific markers that have a standard equivalent (+default, +optional, +required, +listType)",
	Run:      runPreferStandardMarkers,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

// deprecatedMarker pairs a deprecated kubebuilder prefix with its preferred
// standard replacement prefix. Both are matched as prefixes so that the
// marker's value (if any) is preserved in the suggestion.
type deprecatedMarker struct{ from, to string }

var deprecatedMarkers = []deprecatedMarker{
	{"+kubebuilder:default=", "+default="},
	{"+kubebuilder:default:=", "+default="},
	{"+kubebuilder:validation:Optional", "+optional"},
	{"+kubebuilder:validation:Required", "+required"},
	{"+kubebuilder:validation:ListType", "+listType"},
}

func runPreferStandardMarkers(pass *analysis.Pass) (any, error) {
	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	// Check field-level doc and trailing comments.
	ins.Preorder([]ast.Node{(*ast.StructType)(nil)}, func(n ast.Node) {
		st := n.(*ast.StructType)
		if st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			checkCGForDeprecatedMarkers(pass, field.Doc, field.Pos())
			checkCGForDeprecatedMarkers(pass, field.Comment, field.Pos())
		}
	})

	// Check type-level and package-level doc comments.
	for _, f := range pass.Files {
		checkCGForDeprecatedMarkers(pass, f.Doc, f.Pos())
		ast.Inspect(f, func(n ast.Node) bool {
			gd, ok := n.(*ast.GenDecl)
			if ok {
				checkCGForDeprecatedMarkers(pass, gd.Doc, gd.Pos())
			}
			return true
		})
	}

	return nil, nil //nolint:nilnil
}

func checkCGForDeprecatedMarkers(pass *analysis.Pass, cg *ast.CommentGroup, pos token.Pos) {
	if cg == nil {
		return
	}
	for _, c := range cg.List {
		body := markerBody(c.Text)
		for _, dm := range deprecatedMarkers {
			if body == dm.from || len(body) > len(dm.from) && body[:len(dm.from)] == dm.from {
				// Include any value suffix (e.g. "=64") in the suggestion so
				// that "use +default=64 instead of +kubebuilder:default=64" is
				// reported rather than the valueless "use +default= instead of ...".
				valueSuffix := body[len(dm.from):]
				pass.Reportf(pos, "use %s%s instead of %s%s", dm.to, valueSuffix, dm.from, valueSuffix)
				break // one report per line is enough
			}
		}
	}
}
