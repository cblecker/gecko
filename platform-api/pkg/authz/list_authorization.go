package authz

import (
	"context"
	"fmt"

	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// HasNamespacePermission reports whether email has a binding in namespace
// whose referenced role grants action. RoleBinding conditions are deliberately
// not evaluated here: list conditions are applied to each returned object.
func (a *Authorizer) HasNamespacePermission(ctx context.Context, email string, action Action, namespace string) (bool, error) {
	if email == "" {
		return false, fmt.Errorf("principal is empty")
	}
	if namespace == "" {
		return false, nil
	}

	bindings, err := roleBindingsForUser(ctx, a.stores, email)
	if err != nil {
		return false, err
	}

	roles := make(map[string]privatev1.Role)
	platformRoles := make(map[string]privatev1.PlatformRole)
	for _, binding := range bindings {
		if binding.Namespace != namespace {
			continue
		}
		permissions, valid, err := permissionsForBindingFromStores(ctx, a.stores, binding, roles, platformRoles)
		if err != nil {
			return false, fmt.Errorf("resolve RoleBinding %s/%s: %w", binding.Namespace, binding.Name, err)
		}
		if !valid {
			continue
		}
		for _, permission := range permissions {
			bindingAction, known := actionForPermission(permission)
			if known && bindingAction == action {
				return true, nil
			}
		}
	}
	return false, nil
}

func permissionsForBindingFromStores(
	ctx context.Context,
	stores Stores,
	binding privatev1.RoleBinding,
	roles map[string]privatev1.Role,
	platformRoles map[string]privatev1.PlatformRole,
) ([]string, bool, error) {
	if binding.Spec.RoleRef.APIGroup != privatev1.GroupVersion.Group {
		return nil, false, nil
	}

	switch binding.Spec.RoleRef.Kind {
	case "PlatformRole":
		role, found := platformRoles[binding.Spec.RoleRef.Name]
		if !found {
			object, err := stores.PlatformRoles.Get(ctx, "", binding.Spec.RoleRef.Name)
			if apierrors.IsNotFound(err) {
				return nil, false, nil
			}
			if err != nil {
				return nil, false, err
			}
			typed, err := typedStoreObject(stores.Scheme, privatev1.GroupVersion.WithKind("PlatformRole"), object)
			if err != nil {
				return nil, false, err
			}
			typedRole, ok := typed.(*privatev1.PlatformRole)
			if !ok {
				return nil, false, fmt.Errorf("platform role store returned %T", typed)
			}
			role = *typedRole
			platformRoles[role.Name] = role
		}
		return role.Spec.Permissions, true, nil

	case "Role":
		key := binding.Namespace + "/" + binding.Spec.RoleRef.Name
		role, found := roles[key]
		if !found {
			object, err := stores.Roles.Get(ctx, binding.Namespace, binding.Spec.RoleRef.Name)
			if apierrors.IsNotFound(err) {
				return nil, false, nil
			}
			if err != nil {
				return nil, false, err
			}
			typed, err := typedStoreObject(stores.Scheme, privatev1.GroupVersion.WithKind("Role"), object)
			if err != nil {
				return nil, false, err
			}
			typedRole, ok := typed.(*privatev1.Role)
			if !ok {
				return nil, false, fmt.Errorf("role store returned %T", typed)
			}
			role = *typedRole
			roles[key] = role
		}
		return role.Spec.Permissions, true, nil
	default:
		return nil, false, nil
	}
}

func typedStoreObject(scheme *runtime.Scheme, gvk runtimeschema.GroupVersionKind, object client.Object) (client.Object, error) {
	if scheme == nil {
		return nil, fmt.Errorf("scheme is required")
	}
	typedObject, err := scheme.New(gvk)
	if err != nil {
		return nil, fmt.Errorf("create typed %s object: %w", gvk.Kind, err)
	}
	typed, ok := typedObject.(client.Object)
	if !ok {
		return nil, fmt.Errorf("typed %s object does not implement client.Object", gvk.Kind)
	}
	unstructuredObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		return nil, fmt.Errorf("convert %s object to unstructured: %w", gvk.Kind, err)
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredObject, typed); err != nil {
		return nil, fmt.Errorf("convert %s object to typed form: %w", gvk.Kind, err)
	}
	return typed, nil
}
