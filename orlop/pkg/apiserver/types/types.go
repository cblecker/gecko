package types

import (
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
)

// ParentResourceInfo describes a parent resource for nested routing.
type ParentResourceInfo struct {
	Plural  string
	IDField string
}

// PrinterColumn defines a custom column for kubectl output.
type PrinterColumn struct {
	Name        string
	Type        string
	Format      string
	JSONPath    string
	Description string
	Priority    int32
}

// ResourceInfo describes a single API resource type.
type ResourceInfo struct {
	// GVK is the GroupVersionKind for this resource
	GVK runtimeschema.GroupVersionKind
	// Plural is the plural name for the resource (e.g., "objects")
	Plural string
	// Singular is the singular name for the resource (e.g., "object")
	Singular string
	// Namespaced indicates whether the resource is namespace-scoped (true) or cluster-scoped (false).
	Namespaced bool
	// SchemaYAML is the OpenAPI v3 schema in YAML format
	SchemaYAML string
	// ParentResource optionally defines a parent resource for nested routing.
	ParentResource *ParentResourceInfo
	// PrinterColumns defines custom columns for kubectl get output.
	PrinterColumns []PrinterColumn
}
