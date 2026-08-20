package apiserver

import (
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/apply"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/conversion"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/handlers"
	pkgschema "github.com/openshift-online/gecko/orlop/pkg/apiserver/schema"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// ResourceInfo is re-exported from types package for convenience.
type ResourceInfo = types.ResourceInfo

// StorageFactory is a function that creates a storage.ResourceStore for a given resource.
// This allows custom storage backends (PostgreSQL, etc.) to be used instead of the default memory store.
// The resourceType string is derived from the GroupKind and used for database table/channel naming.
type StorageFactory func(resourceType string, scheme *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error)

// GroupKindResourceType returns a stable string identifier for a GroupKind,
// used for database table names, channel names, and error messages.
// Format: "{group}_{kind}" lowercased, e.g. "test.orlop.gcp.managed.openshift.io_object".
func GroupKindResourceType(gk runtimeschema.GroupKind) string {
	return strings.ToLower(gk.Group + "_" + gk.Kind)
}

// ResourceRegistry manages API resource registrations and their stores.
type ResourceRegistry struct {
	resources      []ResourceInfo
	stores         map[runtimeschema.GroupKind]storage.ResourceStore
	storageGVKs    map[runtimeschema.GroupKind]runtimeschema.GroupVersionKind
	scheme         *runtime.Scheme
	storageFactory StorageFactory
	logger         logr.Logger
}

// RegistryOption configures a ResourceRegistry.
type RegistryOption func(*ResourceRegistry)

// WithStorageFactory configures a custom storage factory.
// If not provided, the default in-memory storage is used.
func WithStorageFactory(factory StorageFactory) RegistryOption {
	return func(r *ResourceRegistry) {
		r.storageFactory = factory
	}
}

// WithLogger configures a logger for the registry.
// If not provided, a discard logger is used.
func WithLogger(logger logr.Logger) RegistryOption {
	return func(r *ResourceRegistry) {
		r.logger = logger
	}
}

// NewResourceRegistry creates a new resource registry.
func NewResourceRegistry(scheme *runtime.Scheme, opts ...RegistryOption) *ResourceRegistry {
	r := &ResourceRegistry{
		resources:   []ResourceInfo{},
		stores:      make(map[runtimeschema.GroupKind]storage.ResourceStore),
		storageGVKs: make(map[runtimeschema.GroupKind]runtimeschema.GroupVersionKind),
		scheme:      scheme,
		// Default to in-memory storage
		storageFactory: func(resourceType string, scheme *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error) {
			return memory.NewMemoryStore(resourceType, scheme, gvk), nil
		},
	}

	// Apply options
	for _, opt := range opts {
		opt(r)
	}

	return r
}

// Register adds a resource to the registry and creates its store using the configured storage factory.
// Multiple versions of the same resource (same plural name) share a single store.
// The first registered version becomes the storage version.
func (r *ResourceRegistry) Register(info ResourceInfo) error {
	r.resources = append(r.resources, info)

	gk := info.GVK.GroupKind()
	if _, exists := r.stores[gk]; exists {
		return nil
	}

	resourceType := GroupKindResourceType(gk)
	store, err := r.storageFactory(resourceType, r.scheme, info.GVK)
	if err != nil {
		return fmt.Errorf("failed to create storage for %s: %w", resourceType, err)
	}

	r.stores[gk] = store
	r.storageGVKs[gk] = info.GVK
	return nil
}

// GetStore returns the store for a given GroupKind.
func (r *ResourceRegistry) GetStore(gk runtimeschema.GroupKind) storage.ResourceStore {
	return r.stores[gk]
}

// GetStores returns all stores indexed by GroupKind.
func (r *ResourceRegistry) GetStores() map[runtimeschema.GroupKind]storage.ResourceStore {
	return r.stores
}

// Resources returns all registered resources.
func (r *ResourceRegistry) Resources() []types.ResourceInfo {
	return r.resources
}

// GetResources returns the internal resource list.
func (r *ResourceRegistry) GetResources() []ResourceInfo {
	return r.resources
}

// CreateHandler creates a ResourceHandler for the given resource info.
func (r *ResourceRegistry) CreateHandler(info ResourceInfo) (*handlers.ResourceHandler, error) {
	// Get store for this resource
	gk := info.GVK.GroupKind()
	store := r.GetStore(gk)
	if store == nil {
		return nil, fmt.Errorf("no store found for resource %s", gk)
	}

	// Create schema processor
	processor, err := r.createProcessor(info.SchemaYAML)
	if err != nil {
		return nil, fmt.Errorf("failed to create processor for %s: %w", info.Plural, err)
	}

	// Create handler
	handler := handlers.NewResourceHandler(
		store,
		processor,
		info.GVK,
		info.Plural,
		r.scheme,
		r.logger.WithValues("resource", info.Plural),
	)

	// Set storage GVK for version conversion when serving a non-storage version
	if storageGVK, ok := r.storageGVKs[gk]; ok && storageGVK != info.GVK {
		handler.SetStorageGVK(storageGVK)
	}

	// Create and set apply manager for server-side apply support
	structural, err := schema.NewStructural(processor.GetValidationSchema())
	if err != nil {
		r.logger.Info("Failed to create structural schema, server-side apply disabled", "resource", info.Plural, "error", err)
	} else {
		applyMgr, err := apply.NewManager(r.scheme, structural, info.GVK)
		if err != nil {
			r.logger.Info("Failed to create apply manager, server-side apply disabled", "resource", info.Plural, "error", err)
		} else {
			handler.SetApplyManager(applyMgr)
			r.logger.Info("Server-side apply enabled", "resource", info.Plural)
		}
	}

	return handler, nil
}

// CreateConvertingHandler creates a ConvertingResourceHandler for the given resource info.
func (r *ResourceRegistry) CreateConvertingHandler(converter interface{}, privateScheme *runtime.Scheme, info ResourceInfo) (interface{}, error) {
	// Get store for this resource
	gk := info.GVK.GroupKind()
	store := r.GetStore(gk)
	if store == nil {
		return nil, fmt.Errorf("no store found for resource %s", gk)
	}

	// Create schema processor with public API metadata constraints.
	// Unlike CreateHandler (private API), this injects explicit metadata
	// properties so pruning.Prune() strips private fields like finalizers,
	// ownerReferences, managedFields, etc. from public API inputs.
	processor, err := r.createPublicProcessor(info.SchemaYAML)
	if err != nil {
		return nil, fmt.Errorf("failed to create processor for %s: %w", info.Plural, err)
	}

	// Create converting handler
	handler := handlers.NewConvertingResourceHandler(
		store,
		processor,
		converter.(*conversion.Converter),
		info.GVK,
		info.Plural,
		r.scheme,            // Public scheme from registry
		privateScheme,       // Private scheme passed in
		info.PrinterColumns, // Printer columns for Table format
		r.logger.WithValues("resource", info.Plural),
	)

	return handler, nil
}

// createProcessor creates a schema processor from YAML schema.
// Used by CreateHandler (private API) — no metadata field restrictions.
func (r *ResourceRegistry) createProcessor(schemaYAML string) (*pkgschema.Processor, error) {
	structural, props, err := r.parseSchema(schemaYAML)
	if err != nil {
		return nil, err
	}
	return pkgschema.NewProcessor(structural, props)
}

// createPublicProcessor creates a schema processor with public API metadata constraints.
// Injects explicit metadata properties so pruning.Prune() strips private fields
// (finalizers, ownerReferences, managedFields, deletionTimestamp, etc.) from inputs.
// Used by CreateConvertingHandler (public API) only.
func (r *ResourceRegistry) createPublicProcessor(schemaYAML string) (*pkgschema.Processor, error) {
	structural, props, err := r.parseSchema(schemaYAML)
	if err != nil {
		return nil, err
	}
	injectPublicMetadataSchema(structural)
	return pkgschema.NewProcessor(structural, props)
}

// parseSchema parses YAML schema into structural schema and internal JSONSchemaProps.
func (r *ResourceRegistry) parseSchema(schemaYAML string) (*schema.Structural, *apiext.JSONSchemaProps, error) {
	var propsV1 apiextv1.JSONSchemaProps
	if err := yaml.Unmarshal([]byte(schemaYAML), &propsV1); err != nil {
		return nil, nil, err
	}

	var props apiext.JSONSchemaProps
	if err := apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&propsV1, &props, nil); err != nil {
		return nil, nil, err
	}

	structural, err := schema.NewStructural(&props)
	if err != nil {
		return nil, nil, err
	}

	return structural, &props, nil
}

// injectPublicMetadataSchema constrains the metadata property of a structural schema
// to only allow public-facing ObjectMeta fields. Fields not listed here are pruned
// by K8s pruning.Prune(), preventing clients from setting internal fields like
// finalizers, ownerReferences, managedFields, deletionTimestamp, etc.
func injectPublicMetadataSchema(s *schema.Structural) {
	if s == nil || s.Properties == nil {
		return
	}

	stringType := schema.Structural{
		Generic: schema.Generic{Type: "string"},
	}

	stringMapType := schema.Structural{
		Generic:              schema.Generic{Type: "object"},
		AdditionalProperties: &schema.StructuralOrBool{
			Structural: &schema.Structural{
				Generic: schema.Generic{Type: "string"},
			},
		},
	}

	s.Properties["metadata"] = schema.Structural{
		Generic: schema.Generic{Type: "object"},
		Properties: map[string]schema.Structural{
			"name":            stringType,
			"namespace":       stringType,
			"generateName":    stringType,
			"resourceVersion": stringType,
			"labels":          stringMapType,
			"annotations":     stringMapType,
		},
	}
}
