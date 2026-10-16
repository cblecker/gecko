package authz

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cedar-policy/cedar-go"
	"github.com/go-logr/logr"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

var errInvalidAuthorizationBindingPolicy = errors.New("authorization binding policy is invalid")

// GeneratePolicySet builds the complete Cedar policy set from the current
// authorization resources. Each RoleBinding gets its own policy so bindings
// for the same role cannot accidentally share conditions or principals when
// condition-aware authorization is added.
func GeneratePolicySet(ctx context.Context, stores Stores) (*cedar.PolicySet, error) {
	return generatePolicySet(ctx, stores, logr.Discard())
}

func generatePolicySet(ctx context.Context, stores Stores, logger logr.Logger) (*cedar.PolicySet, error) {
	roles, err := listRoles(ctx, stores)
	if err != nil {
		return nil, fmt.Errorf("list Roles: %w", err)
	}
	platformRoles, err := listPlatformRoles(ctx, stores)
	if err != nil {
		return nil, fmt.Errorf("list PlatformRoles: %w", err)
	}
	bindings, err := listRoleBindings(ctx, stores)
	if err != nil {
		return nil, fmt.Errorf("list RoleBindings: %w", err)
	}

	rolesByNamespace := make(map[string]privatev1.Role, len(roles))
	for _, role := range roles {
		rolesByNamespace[role.Namespace+"/"+role.Name] = role
	}
	platformRolesByName := make(map[string]privatev1.PlatformRole, len(platformRoles))
	for _, role := range platformRoles {
		platformRolesByName[role.Name] = role
	}

	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].Namespace != bindings[j].Namespace {
			return bindings[i].Namespace < bindings[j].Namespace
		}
		return bindings[i].Name < bindings[j].Name
	})

	policies := cedar.NewPolicySet()
	invalidPolicyCount := 0
	for _, binding := range bindings {
		permissions, err := permissionsForBinding(binding, rolesByNamespace, platformRolesByName)
		if err != nil {
			// A dangling or malformed binding must not block policy updates
			// for every other binding. The invalid binding grants nothing.
			logger.Error(err, "skipping invalid authorization binding",
				"namespace", binding.Namespace,
				"binding", binding.Name,
				"roleKind", binding.Spec.RoleRef.Kind,
				"roleName", binding.Spec.RoleRef.Name,
			)
			continue
		}
		policyID := cedar.PolicyID(bindingPolicyID(binding))
		policy, err := policyForBinding(binding, permissions)
		if err != nil {
			// A malformed condition must deny only this binding, not prevent
			// valid bindings from being loaded or updated.
			invalidPolicyCount++
			continue
		}
		policies.Add(policyID, policy)
	}
	if invalidPolicyCount > 0 {
		logger.Error(errInvalidAuthorizationBindingPolicy, "skipped invalid authorization binding policies",
			"reason", "invalid_binding_policy",
			"failureCount", invalidPolicyCount,
		)
	}

	return policies, nil
}

func policyForBinding(binding privatev1.RoleBinding, permissions []string) (*cedar.Policy, error) {
	actions := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		action, ok := actionForPermission(permission)
		if !ok {
			return nil, fmt.Errorf("unknown permission %q", permission)
		}
		actions = append(actions, "Action::"+strconv.Quote(string(action)))
	}
	sort.Strings(actions)

	roleID := fmt.Sprintf("%s/%s/%s", binding.Namespace, binding.Spec.RoleRef.Name, binding.Name)
	conditions := []string{
		fmt.Sprintf("principal in NamespaceRole::%s", strconv.Quote(roleID)),
		fmt.Sprintf("resource in Namespace::%s", strconv.Quote(binding.Namespace)),
	}
	if binding.Spec.Condition != nil && strings.TrimSpace(*binding.Spec.Condition) != "" {
		conditions = append(conditions, "("+*binding.Spec.Condition+")")
	}
	policyText := fmt.Sprintf(`permit (
    principal,
    action in [%s],
    resource
)
when {
    %s
};`, strings.Join(actions, ", "), strings.Join(conditions, " &&\n    "))

	// Require complete consumption of the generated source and exactly one
	// policy; a single-policy parser can ignore trailing policy statements.
	var policies cedar.PolicyList
	if err := policies.UnmarshalCedar([]byte(policyText)); err != nil {
		return nil, err
	}
	if len(policies) != 1 {
		return nil, fmt.Errorf("expected exactly one generated Cedar policy, got %d", len(policies))
	}
	return policies[0], nil
}

func permissionsForBinding(binding privatev1.RoleBinding, roles map[string]privatev1.Role, platformRoles map[string]privatev1.PlatformRole) ([]string, error) {
	if binding.Spec.RoleRef.APIGroup != privatev1.GroupVersion.Group {
		return nil, fmt.Errorf("RoleBinding %s/%s references unsupported API group %q", binding.Namespace, binding.Name, binding.Spec.RoleRef.APIGroup)
	}
	var permissions []string
	switch binding.Spec.RoleRef.Kind {
	case "PlatformRole":
		role, ok := platformRoles[binding.Spec.RoleRef.Name]
		if !ok {
			return nil, fmt.Errorf("RoleBinding %s/%s references missing PlatformRole %q", binding.Namespace, binding.Name, binding.Spec.RoleRef.Name)
		}
		permissions = append(permissions, role.Spec.Permissions...)
	case "Role":
		role, ok := roles[binding.Namespace+"/"+binding.Spec.RoleRef.Name]
		if !ok {
			return nil, fmt.Errorf("RoleBinding %s/%s references missing Role %q", binding.Namespace, binding.Name, binding.Spec.RoleRef.Name)
		}
		permissions = append(permissions, role.Spec.Permissions...)
	default:
		return nil, fmt.Errorf("RoleBinding %s/%s has unsupported roleRef.kind %q", binding.Namespace, binding.Name, binding.Spec.RoleRef.Kind)
	}
	if len(permissions) == 0 {
		return nil, fmt.Errorf("RoleBinding %s/%s references a role with no permissions", binding.Namespace, binding.Name)
	}
	return permissions, nil
}

func bindingPolicyID(binding privatev1.RoleBinding) string {
	prefix := "role"
	if binding.Spec.RoleRef.Kind == "PlatformRole" {
		prefix = "platformrole"
	}
	return fmt.Sprintf("%s:%s:binding:%s/%s", prefix, binding.Spec.RoleRef.Name, binding.Namespace, binding.Name)
}

func listRoles(ctx context.Context, stores Stores) ([]privatev1.Role, error) {
	objects, err := listObjects(ctx, stores.Roles, storage.ListOptions{}, stores.Scheme, privatev1.GroupVersion.WithKind("Role"))
	if err != nil {
		return nil, err
	}
	result := make([]privatev1.Role, 0, len(objects))
	for _, object := range objects {
		role, ok := object.(*privatev1.Role)
		if !ok {
			return nil, fmt.Errorf("role store returned %T", object)
		}
		result = append(result, *role)
	}
	return result, nil
}

func listPlatformRoles(ctx context.Context, stores Stores) ([]privatev1.PlatformRole, error) {
	objects, err := listObjects(ctx, stores.PlatformRoles, storage.ListOptions{}, stores.Scheme, privatev1.GroupVersion.WithKind("PlatformRole"))
	if err != nil {
		return nil, err
	}
	result := make([]privatev1.PlatformRole, 0, len(objects))
	for _, object := range objects {
		role, ok := object.(*privatev1.PlatformRole)
		if !ok {
			return nil, fmt.Errorf("platform role store returned %T", object)
		}
		result = append(result, *role)
	}
	return result, nil
}

func listRoleBindings(ctx context.Context, stores Stores) ([]privatev1.RoleBinding, error) {
	objects, err := listObjects(ctx, stores.RoleBindings, storage.ListOptions{}, stores.Scheme, privatev1.GroupVersion.WithKind("RoleBinding"))
	if err != nil {
		return nil, err
	}
	result := make([]privatev1.RoleBinding, 0, len(objects))
	for _, object := range objects {
		binding, ok := object.(*privatev1.RoleBinding)
		if !ok {
			return nil, fmt.Errorf("role binding store returned %T", object)
		}
		result = append(result, *binding)
	}
	return result, nil
}

func listObjects(ctx context.Context, store storage.ResourceStore, options storage.ListOptions, scheme *runtime.Scheme, gvk runtimeschema.GroupVersionKind) ([]client.Object, error) {
	list, err := store.List(ctx, options)
	if err != nil {
		return nil, err
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return nil, err
	}
	objects := make([]client.Object, 0, len(items))
	for _, item := range items {
		object, ok := item.(client.Object)
		if !ok {
			return nil, fmt.Errorf("list item %T does not implement client.Object", item)
		}
		if scheme != nil {
			typed, err := scheme.New(gvk)
			if err != nil {
				return nil, fmt.Errorf("create typed %s object: %w", gvk.Kind, err)
			}
			unstructuredObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
			if err != nil {
				return nil, fmt.Errorf("convert %s object to unstructured: %w", gvk.Kind, err)
			}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredObject, typed); err != nil {
				return nil, fmt.Errorf("convert %s object to typed form: %w", gvk.Kind, err)
			}
			object, ok = typed.(client.Object)
			if !ok {
				return nil, fmt.Errorf("typed %s object does not implement client.Object", gvk.Kind)
			}
		}
		objects = append(objects, object)
	}
	return objects, nil
}
