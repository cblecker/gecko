package analyzer

import (
	"go/ast"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// ScalarBounds reports struct fields that are missing required scalar bound
// markers. The platform API conventions require:
//
//   - String fields:     +kubebuilder:validation:MaxLength=<N>
//   - Numeric fields:    +kubebuilder:validation:Minimum=<N> or
//     +kubebuilder:validation:Maximum=<N>  (at least one)
//   - Slice fields:      +kubebuilder:validation:MaxItems=<N>
//   - Map fields:        +kubebuilder:validation:MaxProperties=<N>
//
// Only exported fields that carry a JSON struct tag are checked. The check
// verifies that the marker is present; it does not validate the chosen value.
var ScalarBounds = &analysis.Analyzer{
	Name: "scalarbounds",
	Doc: "reports string, numeric, slice, and map fields that are missing " +
		"required scalar bound markers (MaxLength, Minimum/Maximum, MaxItems, MaxProperties)",
	Run:      runScalarBounds,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

func runScalarBounds(pass *analysis.Pass) (any, error) {
	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	ins.Preorder([]ast.Node{(*ast.StructType)(nil)}, func(n ast.Node) {
		st := n.(*ast.StructType)
		if st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			if !fieldHasJSONTag(field) {
				continue
			}
			if len(field.Names) > 0 && !field.Names[0].IsExported() {
				continue
			}

			t := pass.TypesInfo.TypeOf(field.Type)
			if t == nil {
				continue
			}
			t = derefType(t)

			switch {
			case isStringType(t):
				if !fieldHasMarkerPrefix(field, "+kubebuilder:validation:MaxLength=") {
					pass.Reportf(field.Pos(),
						"scalarbounds: string field is missing +kubebuilder:validation:MaxLength")
				}
			case isByteSliceType(t):
				// []byte is serialised by encoding/json as a base64 string, and
				// controller-tools generates a string schema for it. MaxItems
				// does not bound the serialised value; MaxLength is correct.
				if !fieldHasMarkerPrefix(field, "+kubebuilder:validation:MaxLength=") {
					pass.Reportf(field.Pos(),
						"scalarbounds: []byte field is serialised as a base64 string and is missing +kubebuilder:validation:MaxLength")
				}
			case isNumericType(t):
				if !fieldHasMarkerPrefix(field, "+kubebuilder:validation:Minimum=") &&
					!fieldHasMarkerPrefix(field, "+kubebuilder:validation:Maximum=") {
					pass.Reportf(field.Pos(),
						"scalarbounds: numeric field is missing +kubebuilder:validation:Minimum and/or +kubebuilder:validation:Maximum")
				}
			case isSliceType(t):
				if !fieldHasMarkerPrefix(field, "+kubebuilder:validation:MaxItems=") {
					pass.Reportf(field.Pos(),
						"scalarbounds: slice field is missing +kubebuilder:validation:MaxItems")
				}
			case isMapType(t):
				if !fieldHasMarkerPrefix(field, "+kubebuilder:validation:MaxProperties=") {
					pass.Reportf(field.Pos(),
						"scalarbounds: map field is missing +kubebuilder:validation:MaxProperties")
				}
			}
		}
	})
	return nil, nil //nolint:nilnil
}
