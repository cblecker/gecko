package authz

import (
	"context"
	"fmt"

	"github.com/cedar-policy/cedar-go"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
)

func entitiesForUser(ctx context.Context, stores Stores, cache *EntityCache, email string) (cedar.EntityMap, error) {
	if entities, ok := cache.Get(email); ok {
		return entities, nil
	}

	bindings, err := roleBindingsForUser(ctx, stores, email)
	if err != nil {
		return nil, err
	}

	userUID := cedar.NewEntityUID("User", cedar.String(email))
	entities := cedar.EntityMap{
		userUID: {
			UID:        userUID,
			Parents:    cedar.NewEntityUIDSet(),
			Attributes: cedar.NewRecord(nil),
			Tags:       cedar.NewRecord(nil),
		},
	}
	parents := make([]cedar.EntityUID, 0, len(bindings))
	seenNamespaces := make(map[string]struct{})
	for _, binding := range bindings {
		namespaceUID := cedar.NewEntityUID("Namespace", cedar.String(binding.Namespace))
		if _, seen := seenNamespaces[binding.Namespace]; !seen {
			seenNamespaces[binding.Namespace] = struct{}{}
			entities[namespaceUID] = cedar.Entity{
				UID:        namespaceUID,
				Parents:    cedar.NewEntityUIDSet(),
				Attributes: cedar.NewRecord(nil),
				Tags:       cedar.NewRecord(nil),
			}
		}

		roleUID := cedar.NewEntityUID("NamespaceRole", cedar.String(fmt.Sprintf("%s/%s/%s", binding.Namespace, binding.Spec.RoleRef.Name, binding.Name)))
		entities[roleUID] = cedar.Entity{
			UID:        roleUID,
			Parents:    cedar.NewEntityUIDSet(namespaceUID),
			Attributes: cedar.NewRecord(nil),
			Tags:       cedar.NewRecord(nil),
		}
		parents = append(parents, roleUID)
	}
	entities[userUID] = cedar.Entity{
		UID:        userUID,
		Parents:    cedar.NewEntityUIDSet(parents...),
		Attributes: cedar.NewRecord(nil),
		Tags:       cedar.NewRecord(nil),
	}

	cache.Put(email, entities)
	return entities, nil
}

func roleBindingsForUser(ctx context.Context, stores Stores, email string) ([]privatev1.RoleBinding, error) {
	objects, err := listObjects(ctx, stores.RoleBindings, storage.ListOptions{
		FieldFilters: map[string]string{"spec.subject": email},
	}, stores.Scheme, privatev1.GroupVersion.WithKind("RoleBinding"))
	if err != nil {
		return nil, fmt.Errorf("list RoleBindings for principal: %w", err)
	}

	bindings := make([]privatev1.RoleBinding, 0, len(objects))
	for _, object := range objects {
		binding, ok := object.(*privatev1.RoleBinding)
		if !ok {
			return nil, fmt.Errorf("role binding store returned %T", object)
		}
		bindings = append(bindings, *binding)
	}
	return bindings, nil
}
