package analyzer

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// TimestampNaming reports fields of type metav1.Time or *metav1.Time whose
// name ends with "At", "Stamp", or "Timestamp". The platform API conventions
// require timestamp fields to use the suffix "Time" (e.g. decidedAt →
// decidedTime, creationTimestamp → creationTime).
var TimestampNaming = &analysis.Analyzer{
	Name:     "timestampnaming",
	Doc:      `reports metav1.Time fields whose name ends with "At", "Stamp", or "Timestamp"; use the suffix "Time" instead`,
	Run:      runTimestampNaming,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

// badSuffixes lists the suffixes that violate the convention, ordered
// longest-first so that "Timestamp" matches before "Stamp" and "Stamp" matches
// before "At". Without this ordering a field named CreationTimestamp would
// match "Stamp" first, producing the nonsensical suggestion "CreationTimeTime".
var badSuffixes = []string{"Timestamp", "Stamp", "At"}

func runTimestampNaming(pass *analysis.Pass) (any, error) {
	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	nodeFilter := []ast.Node{(*ast.StructType)(nil)}
	ins.Preorder(nodeFilter, func(n ast.Node) {
		st, ok := n.(*ast.StructType)
		if !ok || st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			if !isMetaTime(pass, field) {
				continue
			}
			for _, name := range field.Names {
				for _, suffix := range badSuffixes {
					if strings.HasSuffix(name.Name, suffix) {
						suggested := strings.TrimSuffix(name.Name, suffix) + "Time"
						pass.Reportf(name.Pos(),
							"metav1.Time field %q must end with \"Time\"; use %q instead",
							name.Name, suggested)
						break // report only the longest matching suffix
					}
				}
			}
		}
	})
	return nil, nil //nolint:nilnil
}

// isMetaTime reports whether the field's type is metav1.Time or *metav1.Time.
func isMetaTime(pass *analysis.Pass, field *ast.Field) bool {
	t := pass.TypesInfo.TypeOf(field.Type)
	if t == nil {
		return false
	}
	// Unwrap pointer.
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Name() == "Time" && obj.Pkg() != nil &&
		strings.HasSuffix(obj.Pkg().Path(), "k8s.io/apimachinery/pkg/apis/meta/v1")
}
