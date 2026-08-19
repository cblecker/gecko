package aggregated

import (
	"context"
	"fmt"
	"strings"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/jsonpath"
)

// CustomTableConvertor converts resources to Table format with custom columns.
type CustomTableConvertor struct {
	columns       []types.PrinterColumn
	groupResource schema.GroupResource
}

// NewCustomTableConvertor creates a table convertor with custom columns.
func NewCustomTableConvertor(gr schema.GroupResource, columns []types.PrinterColumn) *CustomTableConvertor {
	return &CustomTableConvertor{
		columns:       columns,
		groupResource: gr,
	}
}

// ConvertToTable converts a resource or list to a Table.
func (c *CustomTableConvertor) ConvertToTable(ctx context.Context, obj runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	table := &metav1.Table{
		TypeMeta: metav1.TypeMeta{
			APIVersion: metav1.SchemeGroupVersion.String(),
			Kind:       "Table",
		},
	}

	// Build column definitions
	table.ColumnDefinitions = c.buildColumnDefinitions()

	// Extract items
	items, err := extractItems(obj)
	if err != nil {
		return nil, err
	}

	// Build rows
	for _, item := range items {
		row, err := c.buildRow(item)
		if err != nil {
			return nil, err
		}
		table.Rows = append(table.Rows, row)
	}

	return table, nil
}

func (c *CustomTableConvertor) buildColumnDefinitions() []metav1.TableColumnDefinition {
	// Always start with Name column
	defs := []metav1.TableColumnDefinition{
		{
			Name:        "Name",
			Type:        "string",
			Format:      "name",
			Description: "Name of the resource",
			Priority:    0,
		},
	}

	// Add custom columns
	for _, col := range c.columns {
		def := metav1.TableColumnDefinition{
			Name:        col.Name,
			Type:        col.Type,
			Format:      col.Format,
			Description: col.Description,
			Priority:    col.Priority,
		}
		defs = append(defs, def)
	}

	return defs
}

func (c *CustomTableConvertor) buildRow(obj runtime.Object) (metav1.TableRow, error) {
	row := metav1.TableRow{
		Object: runtime.RawExtension{Object: obj},
	}

	// Convert to map for JSONPath evaluation
	objMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return row, fmt.Errorf("failed to convert object to unstructured: %w", err)
	}

	// First cell: Name
	nameVal := c.evaluateJSONPath(objMap, ".metadata.name")
	name := ""
	if nameVal != nil {
		name = fmt.Sprint(nameVal)
	}
	row.Cells = append(row.Cells, name)

	// Custom columns
	for _, col := range c.columns {
		value := c.evaluateJSONPath(objMap, col.JSONPath)
		row.Cells = append(row.Cells, value)
	}

	return row, nil
}

// evaluateJSONPath evaluates JSONPath expressions using k8s.io/client-go/util/jsonpath.
// Returns the raw value with preserved type (int, string, bool, etc).
func (c *CustomTableConvertor) evaluateJSONPath(obj map[string]interface{}, path string) interface{} {
	// Wrap path in {}: JSONPath library expects {.path.to.field}
	if !strings.HasPrefix(path, "{") {
		path = "{" + path + "}"
	}

	j := jsonpath.New("cell")
	j.AllowMissingKeys(true) // Don't error on missing fields, return nil

	if err := j.Parse(path); err != nil {
		return nil
	}

	// Use FindResults to get raw typed values instead of string representation
	results, err := j.FindResults(obj)
	if err != nil {
		return nil
	}

	// Extract first result
	if len(results) == 0 || len(results[0]) == 0 {
		return nil
	}

	value := results[0][0]
	if !value.IsValid() || !value.CanInterface() {
		return nil
	}

	return value.Interface()
}

// extractItems returns a slice of runtime.Object from obj (handles both single and list).
func extractItems(obj runtime.Object) ([]runtime.Object, error) {
	if obj == nil {
		return nil, nil
	}

	// Check if it's a List
	items, err := meta.ExtractList(obj)
	if err == nil {
		// Successfully extracted list (may be empty)
		result := make([]runtime.Object, len(items))
		for i, item := range items {
			result[i] = item
		}
		return result, nil
	}

	// Single object
	return []runtime.Object{obj}, nil
}

