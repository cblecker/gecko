package aggregated

import (
	"context"
	"testing"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCustomTableConvertor_ConvertToTable(t *testing.T) {
	gr := schema.GroupResource{Group: "test", Resource: "tests"}
	convertor := NewCustomTableConvertor(gr, []types.PrinterColumn{
		{
			Name:     "Available",
			Type:     "string",
			JSONPath: `.status.conditions[?(@.type=="Ready")].status`,
		},
		{
			Name:     "Age",
			Type:     "date",
			JSONPath: `.metadata.creationTimestamp`,
		},
	})

	testTime := "2024-01-01T00:00:00Z"
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "test/v1",
			"kind":       "Test",
			"metadata": map[string]interface{}{
				"name":              "test-1",
				"creationTimestamp": testTime,
			},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{
						"type":   "Ready",
						"status": "True",
					},
				},
			},
		},
	}

	table, err := convertor.ConvertToTable(context.Background(), obj, nil)
	if err != nil {
		t.Fatalf("ConvertToTable failed: %v", err)
	}

	if len(table.ColumnDefinitions) != 3 { // Name + 2 custom
		t.Errorf("expected 3 columns, got %d", len(table.ColumnDefinitions))
	}

	if table.ColumnDefinitions[0].Name != "Name" {
		t.Errorf("expected first column 'Name', got %q", table.ColumnDefinitions[0].Name)
	}

	if table.ColumnDefinitions[1].Name != "Available" {
		t.Errorf("expected second column 'Available', got %q", table.ColumnDefinitions[1].Name)
	}

	if len(table.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(table.Rows))
	}

	// Check cell values
	row := table.Rows[0]
	if len(row.Cells) != 3 {
		t.Fatalf("expected 3 cells, got %d", len(row.Cells))
	}

	if row.Cells[0] != "test-1" {
		t.Errorf("expected name cell 'test-1', got %v", row.Cells[0])
	}

	if row.Cells[1] != "True" {
		t.Errorf("expected available cell 'True', got %v", row.Cells[1])
	}

	if row.Cells[2] != testTime {
		t.Errorf("expected age cell %q, got %v", testTime, row.Cells[2])
	}
}

func TestEvaluateConditionFilter(t *testing.T) {
	convertor := &CustomTableConvertor{}

	obj := map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"type":   "Ready",
					"status": "True",
				},
				map[string]interface{}{
					"type":   "Available",
					"status": "False",
				},
			},
		},
	}

	tests := []struct {
		name     string
		path     string
		expected interface{}
	}{
		{
			name:     "find Ready condition status",
			path:     `.status.conditions[?(@.type=="Ready")].status`,
			expected: "True",
		},
		{
			name:     "find Available condition status",
			path:     `.status.conditions[?(@.type=="Available")].status`,
			expected: "False",
		},
		{
			name:     "non-existent condition",
			path:     `.status.conditions[?(@.type=="Missing")].status`,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertor.evaluateJSONPath(obj, tt.path)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestEvaluateSimpleJSONPath(t *testing.T) {
	convertor := &CustomTableConvertor{}

	obj := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":              "test-obj",
			"creationTimestamp": "2024-01-01T00:00:00Z",
		},
		"spec": map[string]interface{}{
			"replicas": 3,
		},
	}

	tests := []struct {
		name     string
		path     string
		expected interface{}
	}{
		{
			name:     "simple nested path",
			path:     ".metadata.name",
			expected: "test-obj",
		},
		{
			name:     "timestamp",
			path:     ".metadata.creationTimestamp",
			expected: "2024-01-01T00:00:00Z",
		},
		{
			name:     "integer field",
			path:     ".spec.replicas",
			expected: 3,
		},
		{
			name:     "non-existent field",
			path:     ".status.phase",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertor.evaluateJSONPath(obj, tt.path)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestEmptyList(t *testing.T) {
	gr := schema.GroupResource{Group: "test", Resource: "tests"}
	convertor := NewCustomTableConvertor(gr, []types.PrinterColumn{
		{Name: "Status", Type: "string", JSONPath: ".status"},
	})

	// Create empty list using unstructured
	list := &unstructured.UnstructuredList{
		Object: map[string]interface{}{
			"apiVersion": "test/v1",
			"kind":       "TestList",
		},
		Items: []unstructured.Unstructured{},
	}

	table, err := convertor.ConvertToTable(context.Background(), list, nil)
	if err != nil {
		t.Fatalf("ConvertToTable failed for empty list: %v", err)
	}

	if len(table.Rows) != 0 {
		t.Errorf("expected 0 rows for empty list, got %d", len(table.Rows))
	}

	if len(table.ColumnDefinitions) != 2 { // Name + Status
		t.Errorf("expected 2 column definitions, got %d", len(table.ColumnDefinitions))
	}
}

func TestFormatPreservation(t *testing.T) {
	gr := schema.GroupResource{Group: "test", Resource: "tests"}
	convertor := NewCustomTableConvertor(gr, []types.PrinterColumn{
		{
			Name:     "Count",
			Type:     "integer",
			Format:   "int64",
			JSONPath: ".spec.count",
		},
		{
			Name:     "Ratio",
			Type:     "number",
			Format:   "double",
			JSONPath: ".spec.ratio",
		},
		{
			Name:     "Created",
			Type:     "date",
			Format:   "date-time",
			JSONPath: ".metadata.creationTimestamp",
		},
	})

	table, err := convertor.ConvertToTable(context.Background(), &mockObject{
		gvk: schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Test"},
	}, nil)
	if err != nil {
		t.Fatalf("ConvertToTable failed: %v", err)
	}

	if len(table.ColumnDefinitions) != 4 { // Name + 3 custom
		t.Fatalf("expected 4 columns, got %d", len(table.ColumnDefinitions))
	}

	// Check Format field preservation
	if table.ColumnDefinitions[1].Format != "int64" {
		t.Errorf("expected Count column format 'int64', got %q", table.ColumnDefinitions[1].Format)
	}

	if table.ColumnDefinitions[2].Format != "double" {
		t.Errorf("expected Ratio column format 'double', got %q", table.ColumnDefinitions[2].Format)
	}

	if table.ColumnDefinitions[3].Format != "date-time" {
		t.Errorf("expected Created column format 'date-time', got %q", table.ColumnDefinitions[3].Format)
	}
}

// mockObject implements runtime.Object for testing
type mockObject struct {
	gvk schema.GroupVersionKind
}

func (m *mockObject) GetObjectKind() schema.ObjectKind { return m }
func (m *mockObject) DeepCopyObject() runtime.Object   { return m }
func (m *mockObject) SetGroupVersionKind(gvk schema.GroupVersionKind) {
	m.gvk = gvk
}
func (m *mockObject) GroupVersionKind() schema.GroupVersionKind {
	return m.gvk
}
