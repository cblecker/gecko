package analyzer

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// OptionalHasOmitempty reports exported struct fields that carry a +optional
// marker but are missing omitempty in their JSON tag. The platform API
// conventions state:
//
//	"+optional MUST have omitempty and use a pointer or nilable type."
//
// omitempty is required so that unset optional fields are omitted from
// serialised objects, keeping API responses compact and preventing clients
// from confusing a zero value with an explicitly set value.
//
// The check only applies to exported fields with a JSON struct tag. It does
// not enforce the pointer/nilable-type requirement — that is handled by
// kube-api-linter's optionalfields linter.
var OptionalHasOmitempty = &analysis.Analyzer{
	Name:     "optionalhasomitempty",
	Doc:      "reports +optional fields that are missing omitempty in their JSON tag",
	Run:      runOptionalHasOmitempty,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

func runOptionalHasOmitempty(pass *analysis.Pass) (any, error) {
	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	ins.Preorder([]ast.Node{(*ast.StructType)(nil)}, func(n ast.Node) {
		st := n.(*ast.StructType)
		if st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			// Only check exported, named, JSON-serialised fields.
			// Embedded fields (len(Names)==0) are skipped implicitly: they
			// have no names so Names[0].IsExported() would panic, and they
			// are not the kind of field the convention targets.
			if len(field.Names) == 0 || !field.Names[0].IsExported() {
				continue
			}
			if !fieldHasJSONTag(field) {
				continue
			}
			if !fieldHasExactMarker(field, "+optional") {
				continue
			}
			// Plain (non-pointer) metav1.Time cannot use omitempty: encoding/json
			// does not call IsZero() on plain structs, so omitempty has no effect.
			// The idiomatic fix is *metav1.Time, which kube-api-linter's
			// optionalfields linter already enforces.
			if isMetaTime(pass, field) && !isPointerToMetaTime(pass, field) {
				continue
			}
			if !fieldTagHasOmitempty(field) {
				pass.Reportf(field.Pos(),
					"+optional field must have omitempty in its JSON tag")
			}
		}
	})
	return nil, nil //nolint:nilnil
}

// isPointerToMetaTime reports whether the field's type is *metav1.Time.
func isPointerToMetaTime(pass *analysis.Pass, field *ast.Field) bool {
	t := pass.TypesInfo.TypeOf(field.Type)
	if t == nil {
		return false
	}
	ptr, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Name() == "Time" && obj.Pkg() != nil &&
		strings.HasSuffix(obj.Pkg().Path(), "k8s.io/apimachinery/pkg/apis/meta/v1")
}
