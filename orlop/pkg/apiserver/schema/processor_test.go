package schema_test

import (
	"context"
	"strings"
	"testing"

	pkgschema "github.com/openshift-online/gecko/orlop/pkg/apiserver/schema"

	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"sigs.k8s.io/yaml"
)

func newProcessor(t *testing.T, schemaYAML string) *pkgschema.Processor {
	t.Helper()

	var propsV1 apiextv1.JSONSchemaProps
	if err := yaml.Unmarshal([]byte(schemaYAML), &propsV1); err != nil {
		t.Fatalf("failed to unmarshal YAML: %v", err)
	}

	var props apiext.JSONSchemaProps
	if err := apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&propsV1, &props, nil); err != nil {
		t.Fatalf("failed to convert props: %v", err)
	}

	structural, err := schema.NewStructural(&props)
	if err != nil {
		t.Fatalf("failed to create structural schema: %v", err)
	}

	processor, err := pkgschema.NewProcessor(structural, &props)
	if err != nil {
		t.Fatalf("failed to create processor: %v", err)
	}

	return processor
}

func TestProcess_CELValidation(t *testing.T) {
	const schemaYAML = `
type: object
properties:
  apiVersion:
    type: string
  kind:
    type: string
  metadata:
    type: object
  spec:
    type: object
    properties:
      minReplicas:
        type: integer
      maxReplicas:
        type: integer
    required:
      - minReplicas
      - maxReplicas
    x-kubernetes-validations:
      - rule: "self.minReplicas <= self.maxReplicas"
        message: "minReplicas must be less than or equal to maxReplicas"
`

	processor := newProcessor(t, schemaYAML)

	tests := []struct {
		name     string
		obj      map[string]any
		wantErr  bool
		errorMsg string
	}{
		{
			name: "valid: min <= max",
			obj: map[string]any{
				"apiVersion": "example.com/v1",
				"kind":       "MyResource",
				"metadata":   map[string]any{},
				"spec": map[string]any{
					"minReplicas": int64(1),
					"maxReplicas": int64(5),
				},
			},
		},
		{
			name: "valid: min == max",
			obj: map[string]any{
				"apiVersion": "example.com/v1",
				"kind":       "MyResource",
				"metadata":   map[string]any{},
				"spec": map[string]any{
					"minReplicas": int64(3),
					"maxReplicas": int64(3),
				},
			},
		},
		{
			name: "invalid: min > max",
			obj: map[string]any{
				"apiVersion": "example.com/v1",
				"kind":       "MyResource",
				"metadata":   map[string]any{},
				"spec": map[string]any{
					"minReplicas": int64(10),
					"maxReplicas": int64(5),
				},
			},
			wantErr:  true,
			errorMsg: "minReplicas must be less than or equal to maxReplicas",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := processor.Process(context.Background(), tt.obj, nil)
			if tt.wantErr && len(errs) == 0 {
				t.Fatal("expected validation error, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Fatalf("expected no errors, got: %v", errs)
			}
			if tt.errorMsg != "" {
				assertErrorContains(t, errs, tt.errorMsg)
			}
		})
	}
}

func TestProcess_CELMultipleRules(t *testing.T) {
	const schemaYAML = `
type: object
properties:
  apiVersion:
    type: string
  kind:
    type: string
  metadata:
    type: object
  spec:
    type: object
    properties:
      name:
        type: string
        maxLength: 63
        x-kubernetes-validations:
          - rule: "self.startsWith('app-')"
            message: "name must start with 'app-'"
          - rule: "!self.contains('--')"
            message: "name must not contain consecutive hyphens"
    required:
      - name
`
	processor := newProcessor(t, schemaYAML)

	tests := []struct {
		name     string
		obj      map[string]any
		wantErr  bool
		errorMsg string
	}{
		{
			name: "valid name",
			obj: map[string]any{
				"apiVersion": "example.com/v1",
				"kind":       "MyResource",
				"metadata":   map[string]any{},
				"spec":       map[string]any{"name": "app-frontend"},
			},
		},
		{
			name: "fails prefix rule",
			obj: map[string]any{
				"apiVersion": "example.com/v1",
				"kind":       "MyResource",
				"metadata":   map[string]any{},
				"spec":       map[string]any{"name": "frontend"},
			},
			wantErr:  true,
			errorMsg: "name must start with 'app-'",
		},
		{
			name: "fails consecutive hyphens rule",
			obj: map[string]any{
				"apiVersion": "example.com/v1",
				"kind":       "MyResource",
				"metadata":   map[string]any{},
				"spec":       map[string]any{"name": "app--frontend"},
			},
			wantErr:  true,
			errorMsg: "name must not contain consecutive hyphens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := processor.Process(context.Background(), tt.obj, nil)
			if tt.wantErr && len(errs) == 0 {
				t.Fatal("expected validation error, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Fatalf("expected no errors, got: %v", errs)
			}
			if tt.errorMsg != "" {
				assertErrorContains(t, errs, tt.errorMsg)
			}
		})
	}
}

func TestProcess_NoCELRules(t *testing.T) {
	const schemaYAML = `
type: object
properties:
  apiVersion:
    type: string
  kind:
    type: string
  metadata:
    type: object
  spec:
    type: object
    properties:
      name:
        type: string
    required:
      - name
`
	processor := newProcessor(t, schemaYAML)

	t.Run("valid object passes without CEL rules", func(t *testing.T) {
		obj := map[string]any{
			"apiVersion": "example.com/v1",
			"kind":       "MyResource",
			"metadata":   map[string]any{},
			"spec":       map[string]any{"name": "anything"},
		}
		if errs := processor.Process(context.Background(), obj, nil); len(errs) > 0 {
			t.Fatalf("expected no errors, got: %v", errs)
		}
	})

	t.Run("OpenAPI validation still runs", func(t *testing.T) {
		obj := map[string]any{
			"apiVersion": "example.com/v1",
			"kind":       "MyResource",
			"metadata":   map[string]any{},
			"spec":       map[string]any{},
		}
		errs := processor.Process(context.Background(), obj, nil)
		if len(errs) == 0 {
			t.Fatal("expected validation error for missing required field, got none")
		}
	})
}

func TestProcess_CELWithFieldPath(t *testing.T) {
	const schemaYAML = `
type: object
properties:
  apiVersion:
    type: string
  kind:
    type: string
  metadata:
    type: object
  spec:
    type: object
    properties:
      port:
        type: integer
        x-kubernetes-validations:
          - rule: "self >= 1 && self <= 65535"
            message: "port must be between 1 and 65535"
    required:
      - port
`
	processor := newProcessor(t, schemaYAML)

	tests := []struct {
		name    string
		port    int64
		wantErr bool
	}{
		{"valid port 80", 80, false},
		{"valid port 1", 1, false},
		{"valid port 65535", 65535, false},
		{"invalid port 0", 0, true},
		{"invalid port 70000", 70000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := map[string]any{
				"apiVersion": "example.com/v1",
				"kind":       "MyResource",
				"metadata":   map[string]any{},
				"spec":       map[string]any{"port": tt.port},
			}
			errs := processor.Process(context.Background(), obj, nil)
			if tt.wantErr && len(errs) == 0 {
				t.Fatal("expected validation error, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Fatalf("expected no errors, got: %v", errs)
			}
		})
	}
}

func TestProcess_CELCombinedWithOpenAPIValidation(t *testing.T) {
	const schemaYAML = `
type: object
properties:
  apiVersion:
    type: string
  kind:
    type: string
  metadata:
    type: object
  spec:
    type: object
    properties:
      replicas:
        type: integer
        minimum: 0
        x-kubernetes-validations:
          - rule: "self <= 100"
            message: "replicas must not exceed 100"
    required:
      - replicas
`
	processor := newProcessor(t, schemaYAML)

	tests := []struct {
		name    string
		obj     map[string]any
		wantErr bool
	}{
		{
			name: "passes both OpenAPI and CEL",
			obj: map[string]any{
				"apiVersion": "example.com/v1",
				"kind":       "MyResource",
				"metadata":   map[string]any{},
				"spec":       map[string]any{"replicas": int64(5)},
			},
		},
		{
			name: "fails OpenAPI minimum",
			obj: map[string]any{
				"apiVersion": "example.com/v1",
				"kind":       "MyResource",
				"metadata":   map[string]any{},
				"spec":       map[string]any{"replicas": int64(-1)},
			},
			wantErr: true,
		},
		{
			name: "passes OpenAPI but fails CEL",
			obj: map[string]any{
				"apiVersion": "example.com/v1",
				"kind":       "MyResource",
				"metadata":   map[string]any{},
				"spec":       map[string]any{"replicas": int64(200)},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := processor.Process(context.Background(), tt.obj, nil)
			if tt.wantErr && len(errs) == 0 {
				t.Fatal("expected validation error, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Fatalf("expected no errors, got: %v", errs)
			}
		})
	}
}

func assertErrorContains(t *testing.T, errs field.ErrorList, msg string) {
	t.Helper()
	aggregate := errs.ToAggregate()
	if aggregate == nil {
		t.Fatalf("expected error containing %q, got nil", msg)
	}
	if !strings.Contains(aggregate.Error(), msg) {
		t.Errorf("expected error containing %q, got: %v", msg, aggregate)
	}
}

// TestProcess_OldSelf verifies that CEL rules referencing oldSelf work
// correctly for both create and update paths. A nil oldObj on create causes
// the Kubernetes CEL library to skip transition rules (without optionalOldSelf:true),
// so immutability rules always pass on first creation. On update, oldObj is
// the previous state and the rule fires when the field changes.
func TestProcess_OldSelf_Immutability(t *testing.T) {
	const schemaYAML = `
type: object
properties:
  apiVersion:
    type: string
  kind:
    type: string
  metadata:
    type: object
  spec:
    type: object
    properties:
      clusterID:
        type: string
        x-kubernetes-validations:
          - rule: "self == oldSelf"
            message: "clusterID is immutable"
    required:
      - clusterID
`
	processor := newProcessor(t, schemaYAML)

	obj := func(id string) map[string]any {
		return map[string]any{
			"apiVersion": "example.com/v1",
			"kind":       "MyResource",
			"metadata":   map[string]any{},
			"spec":       map[string]any{"clusterID": id},
		}
	}

	t.Run("create with nil oldObj passes (transition rule skipped)", func(t *testing.T) {
		if errs := processor.Process(context.Background(), obj("cluster-1"), nil); len(errs) > 0 {
			t.Fatalf("expected no error on create, got: %v", errs)
		}
	})

	t.Run("update with same value passes", func(t *testing.T) {
		if errs := processor.Process(context.Background(), obj("cluster-1"), obj("cluster-1")); len(errs) > 0 {
			t.Fatalf("expected no error when value unchanged, got: %v", errs)
		}
	})

	t.Run("update with changed value is rejected", func(t *testing.T) {
		errs := processor.Process(context.Background(), obj("cluster-2"), obj("cluster-1"))
		if len(errs) == 0 {
			t.Fatal("expected immutability error when clusterID changes, got none")
		}
		assertErrorContains(t, errs, "clusterID is immutable")
	})
}

// TestProcess_OldSelf_OnceSettable verifies the "set once" pattern:
// the field may be set when it was previously empty, but may not change
// once it has a value.
func TestProcess_OldSelf_OnceSettable(t *testing.T) {
	const schemaYAML = `
type: object
properties:
  apiVersion:
    type: string
  kind:
    type: string
  metadata:
    type: object
  spec:
    type: object
    properties:
      infraID:
        type: string
        x-kubernetes-validations:
          - rule: "oldSelf == '' || self == oldSelf"
            message: "infraID is immutable once set"
    required:
      - infraID
`
	processor := newProcessor(t, schemaYAML)

	obj := func(id string) map[string]any {
		return map[string]any{
			"apiVersion": "example.com/v1",
			"kind":       "MyResource",
			"metadata":   map[string]any{},
			"spec":       map[string]any{"infraID": id},
		}
	}

	t.Run("create with nil oldObj passes", func(t *testing.T) {
		if errs := processor.Process(context.Background(), obj("infra-abc"), nil); len(errs) > 0 {
			t.Fatalf("expected no error on create, got: %v", errs)
		}
	})

	t.Run("setting value when old was empty passes", func(t *testing.T) {
		if errs := processor.Process(context.Background(), obj("infra-abc"), obj("")); len(errs) > 0 {
			t.Fatalf("expected no error when setting from empty, got: %v", errs)
		}
	})

	t.Run("keeping existing value passes", func(t *testing.T) {
		if errs := processor.Process(context.Background(), obj("infra-abc"), obj("infra-abc")); len(errs) > 0 {
			t.Fatalf("expected no error when value unchanged, got: %v", errs)
		}
	})

	t.Run("changing existing value is rejected", func(t *testing.T) {
		errs := processor.Process(context.Background(), obj("infra-xyz"), obj("infra-abc"))
		if len(errs) == 0 {
			t.Fatal("expected error when infraID changes after being set, got none")
		}
		assertErrorContains(t, errs, "infraID is immutable once set")
	})
}

// TestProcess_OldSelf_NilOldObjDoesNotFireImmutability verifies that passing
// nil as oldObj on create does not cause immutability rules to fire. For rules
// without optionalOldSelf:true, the Kubernetes CEL library skips transition
// rules entirely when oldObj is nil, so create always passes.
func TestProcess_OldSelf_NilOldObjDoesNotFireImmutabilityOnCreate(t *testing.T) {
	const schemaYAML = `
type: object
properties:
  apiVersion:
    type: string
  kind:
    type: string
  metadata:
    type: object
  spec:
    type: object
    properties:
      region:
        type: string
        x-kubernetes-validations:
          - rule: "self == oldSelf"
            message: "region is immutable"
    required:
      - region
`
	processor := newProcessor(t, schemaYAML)

	// This must not return an error: on create there is no old object, and
	// the Kubernetes CEL library skips transition rules when oldObj is nil.
	obj := map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "MyResource",
		"metadata":   map[string]any{},
		"spec":       map[string]any{"region": "us-central1"},
	}
	if errs := processor.Process(context.Background(), obj, nil); len(errs) > 0 {
		t.Fatalf("immutability rule must not fire on create (nil oldObj), got: %v", errs)
	}
}
