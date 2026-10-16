package authz

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
)

func TestAuthorizerDefaultDenyAndNamespaceIsolation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := privatev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	stores, err := NewStores(func(resourceType string, scheme *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error) {
		return memory.NewMemoryStore(resourceType, scheme, gvk), nil
	}, scheme)
	if err != nil {
		t.Fatal(err)
	}

	role := &privatev1.PlatformRole{
		Spec: privatev1.PlatformRoleSpec{Permissions: []string{"cluster.list", "cluster.get"}},
	}
	role.Name = "cluster-viewer"
	role.SetGroupVersionKind(privatev1.GroupVersion.WithKind("PlatformRole"))
	if err := stores.PlatformRoles.Create(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	binding := &privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject: "alice@example.com",
			RoleRef: privatev1.RoleRef{Kind: "PlatformRole", Name: "cluster-viewer", APIGroup: privatev1.GroupVersion.Group},
		},
	}
	binding.Name = "alice-viewer"
	binding.Namespace = "project-a"
	binding.SetGroupVersionKind(privatev1.GroupVersion.WithKind("RoleBinding"))
	if err := stores.RoleBindings.Create(context.Background(), binding); err != nil {
		t.Fatal(err)
	}

	authorizer, err := NewAuthorizer(context.Background(), stores, logr.Discard())
	if err != nil {
		t.Fatal(err)
	}

	allowed, err := authorizer.Authorize(context.Background(), "alice@example.com", GetCluster, "project-a")
	if err != nil || !allowed {
		t.Fatalf("GetCluster project-a = allowed %v, err %v", allowed, err)
	}
	allowed, err = authorizer.Authorize(context.Background(), "alice@example.com", DeleteCluster, "project-a")
	if err != nil || allowed {
		t.Fatalf("DeleteCluster project-a = allowed %v, err %v", allowed, err)
	}
	allowed, err = authorizer.Authorize(context.Background(), "alice@example.com", GetCluster, "project-b")
	if err != nil || allowed {
		t.Fatalf("GetCluster project-b = allowed %v, err %v", allowed, err)
	}
	allowed, err = authorizer.Authorize(context.Background(), "bob@example.com", GetCluster, "project-a")
	if err != nil || allowed {
		t.Fatalf("GetCluster for unbound user = allowed %v, err %v", allowed, err)
	}
}

func TestAuthorizerEvaluatesEffectiveObjectState(t *testing.T) {
	stores, authorizer := newTestAuthorizer(t)
	role := &privatev1.PlatformRole{Spec: privatev1.PlatformRoleSpec{Permissions: []string{"cluster.get"}}}
	role.Name = "cluster-viewer"
	role.SetGroupVersionKind(privatev1.GroupVersion.WithKind("PlatformRole"))
	if err := stores.PlatformRoles.Create(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	condition := `context.resourceName == "east" && context.resourcePlural == "clusters" && context.method == "GET" && context.spec.region == "us-east1"`
	binding := &privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject:   "alice@example.com",
			RoleRef:   privatev1.RoleRef{Kind: "PlatformRole", Name: "cluster-viewer", APIGroup: privatev1.GroupVersion.Group},
			Condition: &condition,
		},
	}
	binding.Name = "east-viewer"
	binding.Namespace = "project-a"
	binding.SetGroupVersionKind(privatev1.GroupVersion.WithKind("RoleBinding"))
	if err := stores.RoleBindings.Create(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	allowedContext, err := cedarContext("east", "clusters", "GET", map[string]any{"region": "us-east1"})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := authorizer.AuthorizeWithContext(context.Background(), "alice@example.com", GetCluster, "project-a", allowedContext)
	if err != nil || !allowed {
		t.Fatalf("AuthorizeWithContext() = allowed %t, err %v; want allowed", allowed, err)
	}

	deniedContext, err := cedarContext("west", "clusters", "GET", map[string]any{"region": "us-west1"})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err = authorizer.AuthorizeWithContext(context.Background(), "alice@example.com", GetCluster, "project-a", deniedContext)
	if err != nil || allowed {
		t.Fatalf("AuthorizeWithContext() = allowed %t, err %v; want denied", allowed, err)
	}
}

func TestAuthorizerAllowsIndependentBindingWhenConditionEvaluationFails(t *testing.T) {
	stores, authorizer := newTestAuthorizer(t)
	role := &privatev1.PlatformRole{Spec: privatev1.PlatformRoleSpec{Permissions: []string{"cluster.get"}}}
	role.Name = "cluster-viewer"
	role.SetGroupVersionKind(privatev1.GroupVersion.WithKind("PlatformRole"))
	if err := stores.PlatformRoles.Create(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	condition := `context.spec.region == "us-east1"`
	for _, tt := range []struct {
		name      string
		condition *string
	}{
		{
			name: "unconditional-viewer",
		},
		{
			name:      "conditioned-viewer",
			condition: &condition,
		},
	} {
		binding := &privatev1.RoleBinding{Spec: privatev1.RoleBindingSpec{
			Subject:   "alice@example.com",
			RoleRef:   privatev1.RoleRef{Kind: "PlatformRole", Name: "cluster-viewer", APIGroup: privatev1.GroupVersion.Group},
			Condition: tt.condition,
		}}
		binding.Name = tt.name
		binding.Namespace = "project-a"
		binding.SetGroupVersionKind(privatev1.GroupVersion.WithKind("RoleBinding"))
		if err := stores.RoleBindings.Create(context.Background(), binding); err != nil {
			t.Fatal(err)
		}
	}
	if err := authorizer.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	requestContext, err := cedarContext("cluster", "clusters", "GET", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := authorizer.AuthorizeWithContext(context.Background(), "alice@example.com", GetCluster, "project-a", requestContext)
	if err != nil || !allowed {
		t.Fatalf("AuthorizeWithContext() = allowed %t, err %v; want allowed", allowed, err)
	}
}

func TestAuthorizerFailsClosedWhenAllMatchingBindingsHaveEvaluationErrors(t *testing.T) {
	stores, authorizer := newTestAuthorizer(t)
	role := &privatev1.PlatformRole{Spec: privatev1.PlatformRoleSpec{Permissions: []string{"cluster.get"}}}
	role.Name = "cluster-viewer"
	role.SetGroupVersionKind(privatev1.GroupVersion.WithKind("PlatformRole"))
	if err := stores.PlatformRoles.Create(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	condition := `context.spec.region == "us-east1"`
	binding := &privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject:   "alice@example.com",
			RoleRef:   privatev1.RoleRef{Kind: "PlatformRole", Name: "cluster-viewer", APIGroup: privatev1.GroupVersion.Group},
			Condition: &condition,
		},
	}
	binding.Name = "conditioned-viewer"
	binding.Namespace = "project-a"
	binding.SetGroupVersionKind(privatev1.GroupVersion.WithKind("RoleBinding"))
	if err := stores.RoleBindings.Create(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	requestContext, err := cedarContext("cluster", "clusters", "GET", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := authorizer.AuthorizeWithContext(context.Background(), "alice@example.com", GetCluster, "project-a", requestContext)
	if err == nil || allowed {
		t.Fatalf("AuthorizeWithContext() = allowed %t, err %v; want denied evaluation error", allowed, err)
	}
}

func TestHasNamespacePermissionIgnoresBindingConditions(t *testing.T) {
	stores, authorizer := newTestAuthorizer(t)
	role := &privatev1.PlatformRole{Spec: privatev1.PlatformRoleSpec{Permissions: []string{"cluster.list"}}}
	role.Name = "cluster-viewer"
	role.SetGroupVersionKind(privatev1.GroupVersion.WithKind("PlatformRole"))
	if err := stores.PlatformRoles.Create(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	condition := "false"
	binding := &privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject:   "alice@example.com",
			RoleRef:   privatev1.RoleRef{Kind: "PlatformRole", Name: "cluster-viewer", APIGroup: privatev1.GroupVersion.Group},
			Condition: &condition,
		},
	}
	binding.Name = "conditioned-viewer"
	binding.Namespace = "project-a"
	binding.SetGroupVersionKind(privatev1.GroupVersion.WithKind("RoleBinding"))
	if err := stores.RoleBindings.Create(context.Background(), binding); err != nil {
		t.Fatal(err)
	}

	if err := authorizer.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	allowed, err := authorizer.HasNamespacePermission(context.Background(), "alice@example.com", ListClusters, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("HasNamespacePermission() = false, want true")
	}
	allowed, err = authorizer.HasNamespacePermission(context.Background(), "alice@example.com", ListClusters, "project-b")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("HasNamespacePermission() = true, want false for project-b")
	}
}

func TestHasNamespacePermissionResolvesNamespacedRole(t *testing.T) {
	stores, authorizer := newTestAuthorizer(t)
	role := &privatev1.Role{Spec: privatev1.RoleSpec{Permissions: []string{"cluster.list"}}}
	role.Name = "cluster-viewer"
	role.Namespace = "project-a"
	role.SetGroupVersionKind(privatev1.GroupVersion.WithKind("Role"))
	if err := stores.Roles.Create(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	binding := &privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject: "alice@example.com",
			RoleRef: privatev1.RoleRef{
				Kind:     "Role",
				Name:     "cluster-viewer",
				APIGroup: privatev1.GroupVersion.Group,
			},
		},
	}
	binding.Name = "namespaced-viewer"
	binding.Namespace = "project-a"
	binding.SetGroupVersionKind(privatev1.GroupVersion.WithKind("RoleBinding"))
	if err := stores.RoleBindings.Create(context.Background(), binding); err != nil {
		t.Fatal(err)
	}

	allowed, err := authorizer.HasNamespacePermission(context.Background(), "alice@example.com", ListClusters, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("HasNamespacePermission() = false, want true")
	}
}

func TestAuthorizerSupportsResourcePluralGuards(t *testing.T) {
	stores, authorizer := newTestAuthorizer(t)
	role := &privatev1.PlatformRole{Spec: privatev1.PlatformRoleSpec{Permissions: []string{"cluster.get", "nodepool.get"}}}
	role.Name = "mixed-viewer"
	role.SetGroupVersionKind(privatev1.GroupVersion.WithKind("PlatformRole"))
	if err := stores.PlatformRoles.Create(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	condition := `context.resourcePlural != "clusters" || context.spec.region == "us-east1"`
	binding := &privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject:   "alice@example.com",
			RoleRef:   privatev1.RoleRef{Kind: "PlatformRole", Name: "mixed-viewer", APIGroup: privatev1.GroupVersion.Group},
			Condition: &condition,
		},
	}
	binding.Name = "mixed-viewer"
	binding.Namespace = "project-a"
	binding.SetGroupVersionKind(privatev1.GroupVersion.WithKind("RoleBinding"))
	if err := stores.RoleBindings.Create(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	clusterContext, err := cedarContext("west", "clusters", "GET", map[string]any{"region": "us-west1"})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := authorizer.AuthorizeWithContext(context.Background(), "alice@example.com", GetCluster, "project-a", clusterContext)
	if err != nil || allowed {
		t.Fatalf("cluster authorization = %t, %v; want denied", allowed, err)
	}

	nodePoolContext, err := cedarContext("workers", "nodepools", "GET", map[string]any{"machineType": "n2-standard-4"})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err = authorizer.AuthorizeWithContext(context.Background(), "alice@example.com", GetNodepool, "project-a", nodePoolContext)
	if err != nil || !allowed {
		t.Fatalf("nodepool authorization = %t, %v; want allowed", allowed, err)
	}
}

func newTestAuthorizer(t *testing.T) (Stores, *Authorizer) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := privatev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	stores, err := NewStores(func(resourceType string, scheme *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error) {
		return memory.NewMemoryStore(resourceType, scheme, gvk), nil
	}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := NewAuthorizer(context.Background(), stores, logr.Discard())
	if err != nil {
		t.Fatal(err)
	}
	return stores, authorizer
}

func TestGeneratePolicySetIsPerBinding(t *testing.T) {
	binding := privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject: "alice@example.com",
			RoleRef: privatev1.RoleRef{Kind: "PlatformRole", Name: "cluster-viewer", APIGroup: privatev1.GroupVersion.Group},
		},
	}
	binding.Name = "viewer"
	binding.Namespace = "project-a"
	role := privatev1.PlatformRole{Spec: privatev1.PlatformRoleSpec{Permissions: []string{"cluster.get"}}}
	role.Name = "cluster-viewer"

	policies, err := policyForBinding(binding, role.Spec.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(policies.MarshalCedar()); got == "" {
		t.Fatal("expected Cedar policy text")
	}
}

func TestGeneratePolicySetSkipsDanglingBinding(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := privatev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	stores, err := NewStores(func(resourceType string, scheme *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error) {
		return memory.NewMemoryStore(resourceType, scheme, gvk), nil
	}, scheme)
	if err != nil {
		t.Fatal(err)
	}

	role := &privatev1.PlatformRole{Spec: privatev1.PlatformRoleSpec{Permissions: []string{"cluster.get"}}}
	role.Name = "cluster-viewer"
	if err := stores.PlatformRoles.Create(context.Background(), role); err != nil {
		t.Fatal(err)
	}

	validBinding := &privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject: "alice@example.com",
			RoleRef: privatev1.RoleRef{Kind: "PlatformRole", Name: "cluster-viewer", APIGroup: privatev1.GroupVersion.Group},
		},
	}
	validBinding.Name = "valid"
	validBinding.Namespace = "project-a"
	if err := stores.RoleBindings.Create(context.Background(), validBinding); err != nil {
		t.Fatal(err)
	}

	danglingBinding := &privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject: "bob@example.com",
			RoleRef: privatev1.RoleRef{Kind: "PlatformRole", Name: "deleted-role", APIGroup: privatev1.GroupVersion.Group},
		},
	}
	danglingBinding.Name = "dangling"
	danglingBinding.Namespace = "project-a"
	if err := stores.RoleBindings.Create(context.Background(), danglingBinding); err != nil {
		t.Fatal(err)
	}

	policies, err := GeneratePolicySet(context.Background(), stores)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(maps.Collect(policies.All())); got != 1 {
		t.Fatalf("policy count = %d, want 1 valid binding policy", got)
	}
}

func TestGeneratePolicySetSkipsMalformedConditionWithoutBlockingValidBindings(t *testing.T) {
	stores, authorizer := newTestAuthorizer(t)
	seedClusterGetBinding(t, stores, "project-a", nil)
	if err := authorizer.Reload(context.Background()); err != nil {
		t.Fatalf("initial policy reload: %v", err)
	}

	malformedCondition := `true }; forbid (principal, action, resource) when { true`
	malformedBinding := &privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject:   "bob@example.com",
			RoleRef:   privatev1.RoleRef{Kind: "PlatformRole", Name: "cluster-viewer", APIGroup: privatev1.GroupVersion.Group},
			Condition: &malformedCondition,
		},
	}
	malformedBinding.Name = "malformed-condition"
	malformedBinding.Namespace = "project-a"
	malformedBinding.SetGroupVersionKind(privatev1.GroupVersion.WithKind("RoleBinding"))
	if err := stores.RoleBindings.Create(context.Background(), malformedBinding); err != nil {
		t.Fatal(err)
	}

	if err := authorizer.Reload(context.Background()); err != nil {
		t.Fatalf("reload with malformed binding: %v; want valid bindings to remain loadable", err)
	}
	allowed, err := authorizer.Authorize(context.Background(), "alice@example.com", GetCluster, "project-a")
	if err != nil || !allowed {
		t.Fatalf("valid binding authorization = %t, %v; want allow", allowed, err)
	}
	allowed, err = authorizer.Authorize(context.Background(), "bob@example.com", GetCluster, "project-a")
	if err != nil || allowed {
		t.Fatalf("malformed binding authorization = %t, %v; want deny", allowed, err)
	}

	if _, err := NewAuthorizer(context.Background(), stores, logr.Discard()); err != nil {
		t.Fatalf("startup with malformed stored binding: %v; want invalid binding skipped", err)
	}
}

func TestGeneratePolicySetLogsMalformedConditionSafely(t *testing.T) {
	stores, _ := newTestAuthorizer(t)
	seedClusterGetBinding(t, stores, "customer-project", nil)

	condition := `true }; forbid (principal, action, resource) when { true`
	binding := &privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject:   "customer@example.com",
			RoleRef:   privatev1.RoleRef{Kind: "PlatformRole", Name: "cluster-viewer", APIGroup: privatev1.GroupVersion.Group},
			Condition: &condition,
		},
	}
	binding.Name = "customer-binding"
	binding.Namespace = "customer-project"
	binding.SetGroupVersionKind(privatev1.GroupVersion.WithKind("RoleBinding"))
	if err := stores.RoleBindings.Create(context.Background(), binding); err != nil {
		t.Fatal(err)
	}

	entries := []capturedAuthorizationLog{}
	logger := logr.FromSlogHandler(&capturedAuthorizationLogHandler{entries: &entries})
	if _, err := generatePolicySet(context.Background(), stores, logger); err != nil {
		t.Fatalf("generate policy set: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want one aggregate log", len(entries))
	}
	entry := entries[0]
	if entry.message != "skipped invalid authorization binding policies" {
		t.Fatalf("log message = %q", entry.message)
	}
	if entry.attributes["reason"] != "invalid_binding_policy" || entry.attributes["failureCount"] != "1" {
		t.Fatalf("log attributes = %v, want sanitized reason and failure count", entry.attributes)
	}
	for _, sensitive := range []string{*binding.Spec.Condition, binding.Namespace, binding.Name, binding.Spec.Subject, binding.Spec.RoleRef.Name} {
		if strings.Contains(entry.String(), sensitive) {
			t.Errorf("authorization log contains customer-controlled value %q: %s", sensitive, entry)
		}
	}
}

func TestAuthorizerRevokesAccessAfterReferencedRoleDeletion(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := privatev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	stores, err := NewStores(func(resourceType string, scheme *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error) {
		return memory.NewMemoryStore(resourceType, scheme, gvk), nil
	}, scheme)
	if err != nil {
		t.Fatal(err)
	}

	role := &privatev1.Role{
		Spec: privatev1.RoleSpec{Permissions: []string{"cluster.get"}},
	}
	role.Name = "cluster-viewer"
	role.Namespace = "project-a"
	role.SetGroupVersionKind(privatev1.GroupVersion.WithKind("Role"))
	if err := stores.Roles.Create(context.Background(), role); err != nil {
		t.Fatal(err)
	}

	binding := &privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject: "alice@example.com",
			RoleRef: privatev1.RoleRef{Kind: "Role", Name: "cluster-viewer", APIGroup: privatev1.GroupVersion.Group},
		},
	}
	binding.Name = "alice-viewer"
	binding.Namespace = "project-a"
	binding.SetGroupVersionKind(privatev1.GroupVersion.WithKind("RoleBinding"))
	if err := stores.RoleBindings.Create(context.Background(), binding); err != nil {
		t.Fatal(err)
	}

	authorizer, err := NewAuthorizer(context.Background(), stores, logr.Discard())
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := authorizer.Authorize(context.Background(), "alice@example.com", GetCluster, "project-a")
	if err != nil || !allowed {
		t.Fatalf("GetCluster before Role deletion = allowed %v, err %v; want allowed", allowed, err)
	}

	if err := stores.Roles.Delete(context.Background(), "project-a", "cluster-viewer"); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	allowed, err = authorizer.Authorize(context.Background(), "alice@example.com", GetCluster, "project-a")
	if err != nil {
		t.Fatalf("GetCluster after Role deletion returned error: %v", err)
	}
	if allowed {
		t.Fatal("GetCluster after Role deletion = allowed, want denied")
	}
}

func TestAuthorizerReloadsAfterRoleBindingChange(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := privatev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	stores, err := NewStores(func(resourceType string, scheme *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error) {
		return memory.NewMemoryStore(resourceType, scheme, gvk), nil
	}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	role := &privatev1.PlatformRole{Spec: privatev1.PlatformRoleSpec{Permissions: []string{"cluster.get"}}}
	role.Name = "cluster-viewer"
	if err := stores.PlatformRoles.Create(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	authorizer, err := NewAuthorizer(context.Background(), stores, logr.Discard())
	if err != nil {
		t.Fatal(err)
	}
	stopCh := make(chan struct{})
	authorizer.StartWatching(stopCh)
	defer close(stopCh)

	binding := &privatev1.RoleBinding{
		Spec: privatev1.RoleBindingSpec{
			Subject: "alice@example.com",
			RoleRef: privatev1.RoleRef{Kind: "PlatformRole", Name: "cluster-viewer", APIGroup: privatev1.GroupVersion.Group},
		},
	}
	binding.Name = "alice-viewer"
	binding.Namespace = "project-a"
	if err := stores.RoleBindings.Create(context.Background(), binding); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		allowed, err := authorizer.Authorize(context.Background(), "alice@example.com", GetCluster, "project-a")
		if err != nil {
			t.Fatal(err)
		}
		if allowed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("RoleBinding change did not become effective")
}
