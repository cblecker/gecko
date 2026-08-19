package aggregated

import (
	"context"
	"fmt"
	"regexp"

	"github.com/go-logr/logr"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	_ rest.Storage              = (*ResourceStorage)(nil)
	_ rest.Scoper               = (*ResourceStorage)(nil)
	_ rest.Getter               = (*ResourceStorage)(nil)
	_ rest.Lister               = (*ResourceStorage)(nil)
	_ rest.Creater              = (*ResourceStorage)(nil)
	_ rest.Updater              = (*ResourceStorage)(nil)
	_ rest.Patcher              = (*ResourceStorage)(nil)
	_ rest.GracefulDeleter      = (*ResourceStorage)(nil)
	_ rest.Watcher              = (*ResourceStorage)(nil)
	_ rest.SingularNameProvider = (*ResourceStorage)(nil)
)

type ResourceStorage struct {
	store          storage.ResourceStore
	strategy       *ResourceStrategy
	gvk            runtimeschema.GroupVersionKind
	plural         string
	singular       string
	scheme         *runtime.Scheme
	groupResource  runtimeschema.GroupResource
	tableConvertor rest.TableConvertor
	logger         logr.Logger
}

func NewResourceStorage(
	store storage.ResourceStore,
	strategy *ResourceStrategy,
	gvk runtimeschema.GroupVersionKind,
	plural, singular string,
	scheme *runtime.Scheme,
	logger logr.Logger,
	printerColumns []types.PrinterColumn,
) *ResourceStorage {
	gr := runtimeschema.GroupResource{Group: gvk.Group, Resource: plural}
	
	var tableConvertor rest.TableConvertor
	if len(printerColumns) > 0 {
		tableConvertor = NewCustomTableConvertor(gr, printerColumns)
	} else {
		tableConvertor = rest.NewDefaultTableConvertor(gr)
	}
	
	return &ResourceStorage{
		store:          store,
		strategy:       strategy,
		gvk:            gvk,
		plural:         plural,
		singular:       singular,
		scheme:         scheme,
		groupResource:  gr,
		tableConvertor: tableConvertor,
		logger:         logger,
	}
}

func (s *ResourceStorage) New() runtime.Object {
	obj, err := s.scheme.New(s.gvk)
	if err != nil {
		panic(fmt.Sprintf("failed to create new object for %v: %v", s.gvk, err))
	}
	return obj
}
func (s *ResourceStorage) Destroy() {}
func (s *ResourceStorage) NewList() runtime.Object {
	listGVK := s.gvk.GroupVersion().WithKind(s.gvk.Kind + "List")
	obj, err := s.scheme.New(listGVK)
	if err != nil {
		panic(fmt.Sprintf("failed to create new list object for %v: %v", listGVK, err))
	}
	return obj
}
func (s *ResourceStorage) NamespaceScoped() bool {
	return s.strategy.NamespaceScoped()
}
func (s *ResourceStorage) GetSingularName() string {
	return s.singular
}

func (s *ResourceStorage) ConvertToTable(ctx context.Context, obj runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return s.tableConvertor.ConvertToTable(ctx, obj, tableOptions)
}

func (s *ResourceStorage) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	namespace, _ := genericapirequest.NamespaceFrom(ctx)
	obj, err := s.store.Get(ctx, namespace, name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, errors.NewNotFound(s.groupResource, name)
		}
		return nil, errors.NewInternalError(fmt.Errorf("failed to get %s %s: %w", s.singular, name, err))
	}
	obj.GetObjectKind().SetGroupVersionKind(s.gvk)
	return obj, nil
}

func (s *ResourceStorage) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	namespace, _ := genericapirequest.NamespaceFrom(ctx)
	opts := storage.ListOptions{
		Namespace: namespace,
	}
	if options != nil {
		if options.LabelSelector != nil {
			opts.LabelSelector = options.LabelSelector.String()
		}
		if options.FieldSelector != nil && !options.FieldSelector.Empty() {
			ff, err := fieldSelectorToFilters(options.FieldSelector)
			if err != nil {
				return nil, errors.NewBadRequest(err.Error())
			}
			opts.FieldFilters = ff
		}
		if options.Continue != "" {
			opts.Continue = options.Continue
		}
		if options.Limit > 0 {
			opts.Limit = options.Limit
		}
	}
	list, err := s.store.List(ctx, opts)
	if err != nil {
		return nil, errors.NewInternalError(fmt.Errorf("failed to list %s: %w", s.plural, err))
	}
	list.GetObjectKind().SetGroupVersionKind(s.gvk.GroupVersion().WithKind(s.gvk.Kind + "List"))
	return list, nil
}

func (s *ResourceStorage) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	s.strategy.PrepareForCreate(ctx, obj)
	if createValidation != nil {
		if err := createValidation(ctx, obj); err != nil {
			return nil, err
		}
	}
	if errs := s.strategy.Validate(ctx, obj); len(errs) > 0 {
		return nil, errors.NewInvalid(s.gvk.GroupKind(), "", errs)
	}
	clientObj, ok := obj.(client.Object)
	if !ok {
		return nil, fmt.Errorf("object does not implement client.Object")
	}
	clientObj.GetObjectKind().SetGroupVersionKind(s.gvk)
	if err := s.store.Create(ctx, clientObj); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil, errors.NewAlreadyExists(s.groupResource, clientObj.GetName())
		}
		return nil, errors.NewInternalError(fmt.Errorf("failed to create %s: %w", s.singular, err))
	}
	return clientObj, nil
}

func (s *ResourceStorage) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	namespace, _ := genericapirequest.NamespaceFrom(ctx)
	existing, err := s.store.Get(ctx, namespace, name)
	if err != nil {
		if errors.IsNotFound(err) {
			if !forceAllowCreate {
				return nil, false, errors.NewNotFound(s.groupResource, name)
			}
			return s.createOnUpdate(ctx, name, namespace, objInfo, createValidation)
		}
		return nil, false, errors.NewInternalError(fmt.Errorf("failed to get %s %s: %w", s.singular, name, err))
	}
	existing.GetObjectKind().SetGroupVersionKind(s.gvk)
	updatedObj, err := objInfo.UpdatedObject(ctx, existing)
	if err != nil {
		return nil, false, err
	}
	s.strategy.PrepareForUpdate(ctx, updatedObj, existing)
	if updateValidation != nil {
		if err := updateValidation(ctx, updatedObj, existing); err != nil {
			return nil, false, err
		}
	}
	if errs := s.strategy.ValidateUpdate(ctx, updatedObj, existing); len(errs) > 0 {
		return nil, false, errors.NewInvalid(s.gvk.GroupKind(), name, errs)
	}
	clientObj, ok := updatedObj.(client.Object)
	if !ok {
		return nil, false, fmt.Errorf("object does not implement client.Object")
	}
	clientObj.GetObjectKind().SetGroupVersionKind(s.gvk)
	if err := s.store.Update(ctx, clientObj); err != nil {
		if errors.IsConflict(err) {
			return nil, false, errors.NewConflict(s.groupResource, name, err)
		}
		return nil, false, errors.NewInternalError(fmt.Errorf("failed to update %s: %w", s.singular, err))
	}

	// If object is marked for deletion and all finalizers removed, hard delete.
	if clientObj.GetDeletionTimestamp() != nil && len(clientObj.GetFinalizers()) == 0 {
		if err := s.store.Delete(ctx, namespace, name); err != nil {
			return nil, false, fmt.Errorf("failed to hard delete %s after finalizer removal: %w", s.singular, err)
		}
		return clientObj, false, nil
	}

	return clientObj, false, nil
}

func (s *ResourceStorage) createOnUpdate(ctx context.Context, name, namespace string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc) (runtime.Object, bool, error) {
	seed := s.New()
	if accessor, aErr := meta.Accessor(seed); aErr == nil {
		accessor.SetName(name)
		accessor.SetNamespace(namespace)
	}
	seed.GetObjectKind().SetGroupVersionKind(s.gvk)
	updatedObj, err := objInfo.UpdatedObject(ctx, seed)
	if err != nil {
		return nil, false, fmt.Errorf("failed to compute new object for create-on-update: %w", err)
	}
	created, err := s.Create(ctx, updatedObj, createValidation, &metav1.CreateOptions{})
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}

func (s *ResourceStorage) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	namespace, _ := genericapirequest.NamespaceFrom(ctx)
	existing, err := s.store.Get(ctx, namespace, name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, false, errors.NewNotFound(s.groupResource, name)
		}
		return nil, false, errors.NewInternalError(fmt.Errorf("failed to get %s %s: %w", s.singular, name, err))
	}
	if deleteValidation != nil {
		if err := deleteValidation(ctx, existing); err != nil {
			return nil, false, err
		}
	}
	if v, ok := existing.(types.CustomValidator); ok {
		if err := v.ValidateDelete(ctx); err != nil {
			return nil, false, errors.NewBadRequest(fmt.Sprintf("validation failed: %v", err))
		}
	}
	clientObj, ok := existing.(client.Object)
	if !ok {
		return nil, false, fmt.Errorf("object does not implement client.Object")
	}

	if options != nil {
		if options.GracePeriodSeconds != nil && *options.GracePeriodSeconds > 0 {
			s.logger.V(1).Info("GracePeriodSeconds not supported, proceeding with immediate deletion",
				"kind", s.gvk.Kind, "namespace", namespace, "name", name,
				"gracePeriodSeconds", *options.GracePeriodSeconds)
		}
		if options.PropagationPolicy != nil {
			switch *options.PropagationPolicy {
			case metav1.DeletePropagationOrphan:
				s.logger.Info("Orphan propagation policy requested — dependents will retain stale ownerReferences",
					"kind", s.gvk.Kind, "namespace", namespace, "name", name)
			case metav1.DeletePropagationForeground:
				s.logger.Info("Foreground propagation policy requested — treated as Background; GC will clean up dependents asynchronously",
					"kind", s.gvk.Kind, "namespace", namespace, "name", name)
			}
		}
	}

	if len(clientObj.GetFinalizers()) > 0 {
		if clientObj.GetDeletionTimestamp() == nil {
			now := metav1.Now()
			clientObj.SetDeletionTimestamp(&now)
			clientObj.GetObjectKind().SetGroupVersionKind(s.gvk)
			if err := s.store.Update(ctx, clientObj); err != nil {
				return nil, false, fmt.Errorf("failed to set deletionTimestamp: %w", err)
			}
		}
		return clientObj, false, nil
	}
	if err := s.store.Delete(ctx, namespace, name); err != nil {
		return nil, false, fmt.Errorf("failed to delete %s: %w", s.singular, err)
	}
	return existing, true, nil
}

func (s *ResourceStorage) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
	namespace, _ := genericapirequest.NamespaceFrom(ctx)
	opts := storage.ListOptions{
		Namespace: namespace,
	}
	var resourceVersion string
	if options != nil {
		if options.LabelSelector != nil {
			opts.LabelSelector = options.LabelSelector.String()
		}
		if options.FieldSelector != nil && !options.FieldSelector.Empty() {
			ff, err := fieldSelectorToFilters(options.FieldSelector)
			if err != nil {
				return nil, errors.NewBadRequest(err.Error())
			}
			opts.FieldFilters = ff
		}
		resourceVersion = options.ResourceVersion
	}

	sendInitialEvents := options != nil && options.SendInitialEvents != nil && *options.SendInitialEvents && options.AllowWatchBookmarks

	if sendInitialEvents {
		list, err := s.store.List(ctx, opts)
		if err != nil {
			return nil, errors.NewInternalError(fmt.Errorf("failed to list %s for initial events: %w", s.plural, err))
		}
		listRV := list.(metav1.ListInterface).GetResourceVersion()

		items, err := meta.ExtractList(list)
		if err != nil {
			return nil, errors.NewInternalError(fmt.Errorf("failed to extract list items: %w", err))
		}

		eventCh, stopFn, err := s.store.Watch(ctx, opts, listRV)
		if err != nil {
			return nil, errors.NewInternalError(fmt.Errorf("failed to watch %s: %w", s.plural, err))
		}

		bookmark, err := s.scheme.New(s.gvk)
		if err != nil {
			stopFn()
			return nil, errors.NewInternalError(fmt.Errorf("failed to create bookmark object: %w", err))
		}
		accessor, err := meta.Accessor(bookmark)
		if err != nil {
			stopFn()
			return nil, errors.NewInternalError(fmt.Errorf("failed to access bookmark metadata: %w", err))
		}
		accessor.SetResourceVersion(listRV)
		accessor.SetAnnotations(map[string]string{
			metav1.InitialEventsAnnotationKey: "true",
		})

		return newInitialEventsWatch(items, bookmark, eventCh, stopFn), nil
	}

	eventCh, stopFn, err := s.store.Watch(ctx, opts, resourceVersion)
	if err != nil {
		return nil, errors.NewInternalError(fmt.Errorf("failed to watch %s: %w", s.plural, err))
	}
	return newWatchAdapter(eventCh, stopFn), nil
}

// StatusStorage handles /status subresource operations.
type StatusStorage struct {
	store *ResourceStorage
}

var (
	_ rest.Storage = (*StatusStorage)(nil)
	_ rest.Getter  = (*StatusStorage)(nil)
	_ rest.Updater = (*StatusStorage)(nil)
)

func NewStatusStorage(store *ResourceStorage) *StatusStorage {
	return &StatusStorage{store: store}
}

func (s *StatusStorage) New() runtime.Object     { return s.store.New() }
func (s *StatusStorage) Destroy()                 {}

func (s *StatusStorage) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	return s.store.Get(ctx, name, options)
}

func (s *StatusStorage) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	namespace, _ := genericapirequest.NamespaceFrom(ctx)
	existing, err := s.store.store.Get(ctx, namespace, name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, false, errors.NewNotFound(s.store.groupResource, name)
		}
		return nil, false, errors.NewInternalError(fmt.Errorf("failed to get %s %s: %w", s.store.singular, name, err))
	}
	existing.GetObjectKind().SetGroupVersionKind(s.store.gvk)
	updatedObj, err := objInfo.UpdatedObject(ctx, existing)
	if err != nil {
		return nil, false, fmt.Errorf("failed to compute updated object: %w", err)
	}

	updatedAccessor, err := meta.Accessor(updatedObj)
	if err != nil {
		return nil, false, fmt.Errorf("failed to access updated object metadata: %w", err)
	}
	existingAccessor, err := meta.Accessor(existing)
	if err != nil {
		return nil, false, fmt.Errorf("failed to access existing object metadata: %w", err)
	}
	if clientRV := updatedAccessor.GetResourceVersion(); clientRV != "" && clientRV != existingAccessor.GetResourceVersion() {
		return nil, false, errors.NewConflict(s.store.groupResource, name, fmt.Errorf("the object has been modified; please apply your changes to the latest version and try again"))
	}

	existingMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(existing)
	if err != nil {
		return nil, false, fmt.Errorf("failed to convert existing object: %w", err)
	}
	updatedMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(updatedObj)
	if err != nil {
		return nil, false, fmt.Errorf("failed to convert updated object: %w", err)
	}
	existingMap["status"] = updatedMap["status"]
	result, err := s.store.scheme.New(s.store.gvk)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create object: %w", err)
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(existingMap, result); err != nil {
		return nil, false, fmt.Errorf("failed to convert merged object: %w", err)
	}

	if updateValidation != nil {
		if err := updateValidation(ctx, result, existing); err != nil {
			return nil, false, err
		}
	}
	if errs := s.store.strategy.ValidateUpdate(ctx, result, existing); len(errs) > 0 {
		return nil, false, errors.NewInvalid(s.store.gvk.GroupKind(), name, errs)
	}
	clientObj, ok := result.(client.Object)
	if !ok {
		return nil, false, fmt.Errorf("object does not implement client.Object")
	}
	clientObj.GetObjectKind().SetGroupVersionKind(s.store.gvk)
	if err := s.store.store.Update(ctx, clientObj); err != nil {
		if errors.IsConflict(err) {
			return nil, false, errors.NewConflict(s.store.groupResource, name, err)
		}
		return nil, false, fmt.Errorf("failed to update status: %w", err)
	}
	return clientObj, false, nil
}

var validFieldPath = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)*$`)

func fieldSelectorToFilters(sel fields.Selector) (map[string]string, error) {
	filters := make(map[string]string)
	for _, req := range sel.Requirements() {
		if req.Operator != selection.Equals && req.Operator != selection.DoubleEquals {
			return nil, fmt.Errorf("unsupported field selector operator %q on field %q; only '=' and '==' are supported", req.Operator, req.Field)
		}
		if !validFieldPath.MatchString(req.Field) {
			return nil, fmt.Errorf("invalid field selector path %q", req.Field)
		}
		filters[req.Field] = req.Value
	}
	return filters, nil
}
