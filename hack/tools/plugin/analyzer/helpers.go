package analyzer

import (
	"go/ast"
	"go/types"
	"reflect"
	"strconv"
	"strings"
)

// ── Comment normalisation ─────────────────────────────────────────────────────

// markerBody strips the leading "//" from a comment line and trims surrounding
// whitespace, normalising both the "// +marker" and "//+marker" forms that
// controller-gen accepts.
func markerBody(commentText string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(commentText), "//"))
}

// cgHasExactMarker reports whether any line in cg, after normalisation,
// equals marker (e.g. "+required", "+immutable").
func cgHasExactMarker(cg *ast.CommentGroup, marker string) bool {
	if cg == nil {
		return false
	}
	for _, c := range cg.List {
		if markerBody(c.Text) == marker {
			return true
		}
	}
	return false
}

// cgHasMarkerPrefix reports whether any line in cg, after normalisation,
// has the given prefix (e.g. "+kubebuilder:validation:MaxLength=").
func cgHasMarkerPrefix(cg *ast.CommentGroup, prefix string) bool {
	if cg == nil {
		return false
	}
	for _, c := range cg.List {
		if strings.HasPrefix(markerBody(c.Text), prefix) {
			return true
		}
	}
	return false
}

// fieldHasExactMarker reports whether either the doc comment or the trailing
// inline comment of a field contains the given marker.
func fieldHasExactMarker(field *ast.Field, marker string) bool {
	return cgHasExactMarker(field.Doc, marker) || cgHasExactMarker(field.Comment, marker)
}

// fieldHasMarkerPrefix reports whether either the doc comment or the trailing
// inline comment of a field contains a line starting with the given prefix.
func fieldHasMarkerPrefix(field *ast.Field, prefix string) bool {
	return cgHasMarkerPrefix(field.Doc, prefix) || cgHasMarkerPrefix(field.Comment, prefix)
}

// ── JSON tag helpers ──────────────────────────────────────────────────────────

// rawTag returns the unquoted content of a struct tag literal. The AST stores
// tag values either as backtick-quoted strings (`json:"name"`) or as
// double-quoted strings ("json:\"name\""). Both forms are handled so that
// reflect.StructTag.Lookup works correctly on the result.
func rawTag(tagLit string) string {
	if len(tagLit) >= 2 && tagLit[0] == '`' {
		return tagLit[1 : len(tagLit)-1]
	}
	// Double-quoted form: use strconv.Unquote to process escape sequences.
	if s, err := strconv.Unquote(tagLit); err == nil {
		return s
	}
	return tagLit
}

// fieldTagHasOmitempty reports whether the field's JSON struct tag contains
// the "omitempty" option.
func fieldTagHasOmitempty(field *ast.Field) bool {
	if field.Tag == nil {
		return false
	}
	jsonTag, ok := reflect.StructTag(rawTag(field.Tag.Value)).Lookup("json")
	if !ok {
		return false
	}
	for _, part := range strings.Split(jsonTag, ",")[1:] {
		if part == "omitempty" {
			return true
		}
	}
	return false
}

// fieldHasJSONTag reports whether the field participates in JSON serialisation.
// A field is excluded only when its json tag name is "-". An empty tag name
// (json:"") means the field serialises under its Go field name, so it is
// included. Fields with no json key at all are excluded.
func fieldHasJSONTag(field *ast.Field) bool {
	if field.Tag == nil {
		return false
	}
	tag, ok := reflect.StructTag(rawTag(field.Tag.Value)).Lookup("json")
	if !ok {
		return false
	}
	// The first comma-separated element is the field name (or "-" to exclude).
	name := strings.SplitN(tag, ",", 2)[0]
	return name != "-"
}

// ── Type helpers ──────────────────────────────────────────────────────────────

// derefType unwraps a pointer type, returning the element type. Non-pointer
// types are returned unchanged.
func derefType(t types.Type) types.Type {
	if ptr, ok := t.(*types.Pointer); ok {
		return ptr.Elem()
	}
	return t
}

// isStringType reports whether t is the built-in string type.
func isStringType(t types.Type) bool {
	basic, ok := t.Underlying().(*types.Basic)
	return ok && basic.Kind() == types.String
}

// isNumericType reports whether t is int32 or int64 — the only integer types
// permitted in Kubernetes APIs (floats and plain int are already forbidden by
// kube-api-linter).
func isNumericType(t types.Type) bool {
	basic, ok := t.Underlying().(*types.Basic)
	if !ok {
		return false
	}
	return basic.Kind() == types.Int32 || basic.Kind() == types.Int64
}

// isByteSliceType reports whether t is []byte. encoding/json serialises []byte
// as a base64 string, so controller-tools generates a string schema for it.
// MaxLength (not MaxItems) is the appropriate bound.
func isByteSliceType(t types.Type) bool {
	sl, ok := t.Underlying().(*types.Slice)
	if !ok {
		return false
	}
	elem, ok := sl.Elem().Underlying().(*types.Basic)
	return ok && elem.Kind() == types.Byte
}

// isSliceType reports whether t is a slice type (excluding []byte which has
// string serialisation semantics — see isByteSliceType).
func isSliceType(t types.Type) bool {
	if isByteSliceType(t) {
		return false
	}
	_, ok := t.Underlying().(*types.Slice)
	return ok
}

// isMapType reports whether t is a map type.
func isMapType(t types.Type) bool {
	_, ok := t.Underlying().(*types.Map)
	return ok
}
