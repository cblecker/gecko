package aggregated

import (
	"context"
	"testing"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCustomTableConvertor_ConvertToTable(t *testing.T) {
	columns := []types.PrinterColumn{
		{Name: "Available", Type: "string", JSONPath: `.status.conditions[?(@.type=="Ready")].status`},
		{Name: "Age", Type: "date", JSONPath: `.metadata.creationTimestamp`},
	}

	convertor := NewCustomTableConvertor(
		schema.GroupResource{Group: "test.io", Resource: "tests"},
		columns,
	)

	// Create test object with conditions
	testTime := "2024-01-01T00:00:00Z"
	obj := &runtime.Unknown{}
	objMap := map[string]interface{}{
		"apiVersion": "test.io/v1",
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
	}
	runtime.DefaultUnstructuredConverter.FromUnstructured(objMap, obj)

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
