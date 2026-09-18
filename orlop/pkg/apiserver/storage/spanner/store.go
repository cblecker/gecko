package spanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SpannerStore struct {
	client           *spanner.Client
	resourceType     string
	scheme           *runtime.Scheme
	gvk              schema.GroupVersionKind
	broadcaster      storage.EventBroadcaster
	tableName        string
	countersTable    string
	counterID        string
	contextFilterKey any
	logger           logr.Logger
}

type SpannerStoreConfig struct {
	Client           *spanner.Client
	ResourceType     string
	Scheme           *runtime.Scheme
	GVK              schema.GroupVersionKind
	Broadcaster      storage.EventBroadcaster
	TableName        string
	CountersTable    string
	ContextFilterKey any
	Logger           logr.Logger
}

func NewSpannerStore(config SpannerStoreConfig) (*SpannerStore, error) {
	if config.Client == nil {
		return nil, fmt.Errorf("spanner client is required")
	}
	if config.ResourceType == "" {
		return nil, fmt.Errorf("resource type is required")
	}
	if config.Scheme == nil {
		return nil, fmt.Errorf("scheme is required")
	}

	tableName := config.TableName
	if tableName == "" {
		tableName = "resources"
	}
	countersTable := config.CountersTable
	if countersTable == "" {
		countersTable = "counters"
	}

	storeLogger := config.Logger
	if storeLogger.GetSink() == nil {
		storeLogger = logr.Discard()
	}

	return &SpannerStore{
		client:           config.Client,
		resourceType:     config.ResourceType,
		scheme:           config.Scheme,
		gvk:              config.GVK,
		broadcaster:      config.Broadcaster,
		tableName:        tableName,
		countersTable:    countersTable,
		counterID:        config.ResourceType,
		contextFilterKey: config.ContextFilterKey,
		logger:           storeLogger,
	}, nil
}

func (s *SpannerStore) contextFilterValue(ctx context.Context) (string, error) {
	if s.contextFilterKey == nil {
		return "", nil
	}
	v := ctx.Value(s.contextFilterKey)
	if v == nil {
		return "", fmt.Errorf("context filter key %v not found in context", s.contextFilterKey)
	}
	str, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("context filter value must be a string, got %T", v)
	}
	return str, nil
}

func marshalData(obj client.Object) ([]byte, error) {
	rv := obj.GetResourceVersion()
	obj.SetResourceVersion("")
	data, err := json.Marshal(obj)
	obj.SetResourceVersion(rv)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal object: %w", err)
	}
	return data, nil
}

func unmarshalData(data []byte, rv int64) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, obj); err != nil {
		return nil, fmt.Errorf("failed to unmarshal object: %w", err)
	}
	obj.SetResourceVersion(strconv.FormatInt(rv, 10))
	return obj, nil
}

// unmarshalTyped deserialises data into a typed object using the store's
// scheme and GVK. If the GVK is not registered in the scheme it falls back
// to *unstructured.Unstructured so callers always receive a valid client.Object.
func (s *SpannerStore) unmarshalTyped(data []byte, rv int64) (client.Object, error) {
	rvStr := strconv.FormatInt(rv, 10)

	if s.scheme == nil || s.gvk.Empty() {
		return unmarshalData(data, rv)
	}

	robj, err := s.scheme.New(s.gvk)
	if err != nil {
		// GVK not registered — fall back to unstructured.
		return unmarshalData(data, rv)
	}

	if err := json.Unmarshal(data, robj); err != nil {
		return nil, fmt.Errorf("failed to unmarshal into typed object: %w", err)
	}

	cobj, ok := robj.(client.Object)
	if !ok {
		return nil, fmt.Errorf("scheme returned object that does not implement client.Object")
	}
	cobj.SetResourceVersion(rvStr)
	return cobj, nil
}

func (s *SpannerStore) incrementCounter(txn *spanner.ReadWriteTransaction, ctx context.Context) (int64, error) {
	row, err := txn.ReadRow(ctx, s.countersTable, spanner.Key{s.counterID}, []string{"value"})
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound {
			return 1, nil
		}
		return 0, err
	}
	var current int64
	if err := row.Column(0, &current); err != nil {
		return 0, err
	}
	return current + 1, nil
}

func (s *SpannerStore) Create(ctx context.Context, obj client.Object) error {
	filterValue, err := s.contextFilterValue(ctx)
	if err != nil {
		return err
	}

	namespace := obj.GetNamespace()
	name := obj.GetName()
	useGenerateName := name == "" && obj.GetGenerateName() != ""

	maxAttempts := 1
	if useGenerateName {
		maxAttempts = 5
	}

	for attempt := range maxAttempts {
		if useGenerateName {
			name = storage.GenerateName(obj.GetGenerateName())
			obj.SetName(name)
		}

		uid := uuid.NewString()
		obj.SetUID(types.UID(uid))

		now := time.Now()
		creationTime := obj.GetCreationTimestamp()
		if creationTime.IsZero() {
			obj.SetCreationTimestamp(metav1.NewTime(now))
		}

		data, err := marshalData(obj)
		if err != nil {
			return err
		}

		labelsJSON, err := json.Marshal(obj.GetLabels())
		if err != nil {
			return fmt.Errorf("failed to marshal labels: %w", err)
		}

		var rv int64
		_, err = s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
			rv = 0
			var counterErr error
			rv, counterErr = s.incrementCounter(txn, ctx)
			if counterErr != nil {
				return counterErr
			}

			var deletionTs *time.Time
			if dt := obj.GetDeletionTimestamp(); dt != nil {
				t := dt.Time
				deletionTs = &t
			}

			return txn.BufferWrite([]*spanner.Mutation{
				spanner.InsertOrUpdate(s.countersTable, []string{"counter_id", "value"}, []any{s.counterID, rv}),
				spanner.Insert(s.tableName,
					[]string{"resource_type", "context_filter", "namespace", "name", "uid", "resource_version", "object_version", "labels", "data", "deletion_timestamp", "created_at", "updated_at"},
					[]any{s.resourceType, filterValue, namespace, name, uid, rv, int64(1), spanner.NullJSON{Value: json.RawMessage(labelsJSON), Valid: true}, spanner.NullJSON{Value: json.RawMessage(data), Valid: true}, deletionTs, now, now},
				),
			})
		})
		if err != nil {
			if spanner.ErrCode(err) == codes.AlreadyExists {
				if useGenerateName && attempt < maxAttempts-1 {
					continue
				}
				return apierrors.NewAlreadyExists(
					schema.GroupResource{Resource: s.resourceType},
					name,
				)
			}
			return fmt.Errorf("failed to create object: %w", err)
		}

		obj.SetResourceVersion(strconv.FormatInt(rv, 10))
		return nil
	}

	return fmt.Errorf("failed to generate unique name after retries")
}

func (s *SpannerStore) Get(ctx context.Context, namespace, name string) (client.Object, error) {
	filterValue, err := s.contextFilterValue(ctx)
	if err != nil {
		return nil, err
	}

	row, err := s.client.Single().ReadRow(ctx, s.tableName,
		spanner.Key{s.resourceType, filterValue, namespace, name},
		[]string{"data", "resource_version"},
	)
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound {
			return nil, apierrors.NewNotFound(
				schema.GroupResource{Resource: s.resourceType},
				name,
			)
		}
		return nil, fmt.Errorf("failed to get object: %w", err)
	}

	var dataJSON spanner.NullJSON
	var rv int64
	if err := row.Columns(&dataJSON, &rv); err != nil {
		return nil, fmt.Errorf("failed to scan row: %w", err)
	}

	dataBytes, err := json.Marshal(dataJSON.Value)
	if err != nil {
		return nil, fmt.Errorf("failed to re-marshal data: %w", err)
	}

	obj, err := unmarshalData(dataBytes, rv)
	if err != nil {
		return nil, err
	}

	return obj, nil
}

func (s *SpannerStore) List(ctx context.Context, opts storage.ListOptions) (client.ObjectList, error) {
	filterValue, err := s.contextFilterValue(ctx)
	if err != nil {
		return nil, err
	}

	if opts.Continue != "" {
		if _, err := storage.DecodeContinueToken(opts.Continue); err != nil {
			return nil, apierrors.NewResourceExpired("invalid continue token")
		}
	}

	qb := newQueryBuilder(s.tableName, "namespace", "name", "resource_version", "data")
	qb.whereResourceType(s.resourceType)

	if s.contextFilterKey != nil {
		qb.whereContextFilter(filterValue)
	}
	qb.whereNamespace(opts.Namespace)

	if opts.LabelSelector != "" {
		selector, err := labels.Parse(opts.LabelSelector)
		if err != nil {
			return nil, err
		}
		qb.whereLabelSelector(selector)
	}

	qb.whereShardSelector(opts.ShardSelector)
	qb.whereFieldFilters(opts.FieldFilters)

	if opts.Continue != "" {
		token, _ := storage.DecodeContinueToken(opts.Continue)
		qb.whereContinueToken(token)
	}

	qb.setOrderBy("namespace", "name")

	if opts.Limit > 0 {
		qb.setLimit(opts.Limit + 1)
	}

	query, params := qb.build()
	stmt := spanner.Statement{SQL: query, Params: params}

	txn := s.client.ReadOnlyTransaction()
	defer txn.Close()

	iter := txn.Query(ctx, stmt)
	defer iter.Stop()

	var items []client.Object
	rowCount := int64(0)

	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate rows: %w", err)
		}

		rowCount++

		if opts.Limit > 0 && rowCount > opts.Limit {
			break
		}

		var namespace, name string
		var rv int64
		var dataJSON spanner.NullJSON
		if err := row.Columns(&namespace, &name, &rv, &dataJSON); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		dataBytes, err := json.Marshal(dataJSON.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to re-marshal data: %w", err)
		}

		// unmarshalTyped returns a typed object when the store's scheme has
		// the GVK registered, falling back to *unstructured.Unstructured.
		// Typed objects are required so meta.SetList can assign them into a
		// scheme-typed list's Items slice (e.g. []v1.Cluster) without a
		// reflect type-mismatch error.
		obj, err := s.unmarshalTyped(dataBytes, rv)
		if err != nil {
			return nil, err
		}

		items = append(items, obj)
	}

	counterRow, err := txn.ReadRow(ctx, s.countersTable, spanner.Key{s.counterID}, []string{"value"})
	var globalRV int64
	if err != nil {
		if spanner.ErrCode(err) != codes.NotFound {
			return nil, fmt.Errorf("failed to read global resource version: %w", err)
		}
	} else {
		if err := counterRow.Column(0, &globalRV); err != nil {
			return nil, fmt.Errorf("failed to read global resource version: %w", err)
		}
	}

	listGVK := s.gvk.GroupVersion().WithKind(s.gvk.Kind + "List")
	listObj, err := s.scheme.New(listGVK)
	if err != nil {
		return nil, fmt.Errorf("failed to create list object: %w", err)
	}

	listAccessor, err := meta.ListAccessor(listObj)
	if err != nil {
		return nil, fmt.Errorf("object does not support list operations: %w", err)
	}
	listAccessor.SetResourceVersion(strconv.FormatInt(globalRV, 10))

	if uList, ok := listObj.(*unstructured.UnstructuredList); ok {
		for _, obj := range items {
			if u, ok := obj.(*unstructured.Unstructured); ok {
				uList.Items = append(uList.Items, *u)
			}
		}
	} else {
		runtimeItems := make([]runtime.Object, len(items))
		for i, obj := range items {
			runtimeItems[i] = obj
		}
		if err := meta.SetList(listObj, runtimeItems); err != nil {
			return nil, fmt.Errorf("failed to set list items: %w", err)
		}
	}

	hasMore := opts.Limit > 0 && rowCount > opts.Limit
	if hasMore && len(items) > 0 {
		lastItem := items[len(items)-1]
		token := &storage.ContinueToken{
			Namespace:       lastItem.GetNamespace(),
			Name:            lastItem.GetName(),
			ResourceVersion: strconv.FormatInt(globalRV, 10),
		}
		continueStr, err := storage.EncodeContinueToken(token)
		if err == nil {
			listAccessor.SetContinue(continueStr)
		}
	}

	return listObj.(client.ObjectList), nil
}

func (s *SpannerStore) Update(ctx context.Context, obj client.Object) error {
	filterValue, err := s.contextFilterValue(ctx)
	if err != nil {
		return err
	}

	namespace := obj.GetNamespace()
	name := obj.GetName()

	data, err := marshalData(obj)
	if err != nil {
		return err
	}

	labelsJSON, err := json.Marshal(obj.GetLabels())
	if err != nil {
		return fmt.Errorf("failed to marshal labels: %w", err)
	}

	expectedRV, _ := strconv.ParseInt(obj.GetResourceVersion(), 10, 64)

	var rv int64
	_, err = s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		rv = 0

		row, readErr := txn.ReadRow(ctx, s.tableName,
			spanner.Key{s.resourceType, filterValue, namespace, name},
			[]string{"resource_version", "object_version"},
		)
		if readErr != nil {
			if spanner.ErrCode(readErr) == codes.NotFound {
				return apierrors.NewNotFound(schema.GroupResource{Resource: s.resourceType}, name)
			}
			return readErr
		}

		var storedRV, objectVersion int64
		if err := row.Columns(&storedRV, &objectVersion); err != nil {
			return err
		}
		if expectedRV != 0 && storedRV != expectedRV {
			return apierrors.NewConflict(schema.GroupResource{Resource: s.resourceType}, name,
				fmt.Errorf("resource version %d does not match %d", expectedRV, storedRV))
		}

		var counterErr error
		rv, counterErr = s.incrementCounter(txn, ctx)
		if counterErr != nil {
			return counterErr
		}

		var deletionTs *time.Time
		if dt := obj.GetDeletionTimestamp(); dt != nil {
			t := dt.Time
			deletionTs = &t
		}

		now := time.Now()
		return txn.BufferWrite([]*spanner.Mutation{
			spanner.InsertOrUpdate(s.countersTable, []string{"counter_id", "value"}, []any{s.counterID, rv}),
			spanner.Update(s.tableName,
				[]string{"resource_type", "context_filter", "namespace", "name", "resource_version", "object_version", "labels", "data", "deletion_timestamp", "updated_at"},
				[]any{s.resourceType, filterValue, namespace, name, rv, objectVersion + 1, spanner.NullJSON{Value: json.RawMessage(labelsJSON), Valid: true}, spanner.NullJSON{Value: json.RawMessage(data), Valid: true}, deletionTs, now},
			),
		})
	})
	if err != nil {
		return fmt.Errorf("failed to update object: %w", err)
	}

	obj.SetResourceVersion(strconv.FormatInt(rv, 10))
	return nil
}

func (s *SpannerStore) Delete(ctx context.Context, namespace, name string) error {
	filterValue, err := s.contextFilterValue(ctx)
	if err != nil {
		return err
	}

	_, err = s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		_, readErr := txn.ReadRow(ctx, s.tableName,
			spanner.Key{s.resourceType, filterValue, namespace, name},
			[]string{"resource_version"},
		)
		if readErr != nil {
			if spanner.ErrCode(readErr) == codes.NotFound {
				return apierrors.NewNotFound(schema.GroupResource{Resource: s.resourceType}, name)
			}
			return readErr
		}

		rv, counterErr := s.incrementCounter(txn, ctx)
		if counterErr != nil {
			return counterErr
		}

		return txn.BufferWrite([]*spanner.Mutation{
			spanner.InsertOrUpdate(s.countersTable, []string{"counter_id", "value"}, []any{s.counterID, rv}),
			spanner.Delete(s.tableName, spanner.Key{s.resourceType, filterValue, namespace, name}),
		})
	})
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}

	return nil
}

func (s *SpannerStore) Watch(ctx context.Context, opts storage.ListOptions, resourceVersion string) (<-chan storage.ResourceEvent, func(), error) {
	filterValue, err := s.contextFilterValue(ctx)
	if err != nil {
		return nil, nil, err
	}

	if s.broadcaster == nil {
		return nil, nil, fmt.Errorf("broadcaster not configured")
	}

	eventCh, stopSubscription, err := s.broadcaster.Subscribe(resourceVersion)
	if err != nil {
		return nil, nil, err
	}

	outCh := make(chan storage.ResourceEvent, 100)
	stopCh := make(chan struct{})

	go func() {
		defer close(outCh)
		defer stopSubscription()

		var labelSelector labels.Selector
		if opts.LabelSelector != "" {
			var err error
			labelSelector, err = labels.Parse(opts.LabelSelector)
			if err != nil {
				labelSelector = nil
			}
		}

		for {
			select {
			case <-stopCh:
				return
			case event, ok := <-eventCh:
				if !ok {
					return
				}

				if s.contextFilterKey != nil {
					if event.ContextFilterValue != filterValue {
						continue
					}
				}

				if opts.Namespace != "" && event.Object.GetNamespace() != opts.Namespace {
					continue
				}

				if labelSelector != nil && !labelSelector.Matches(labels.Set(event.Object.GetLabels())) {
					continue
				}

				if opts.ShardSelector != nil {
					matches, err := storage.MatchesShard(event.Object, opts.ShardSelector)
					if err != nil || !matches {
						continue
					}
				}

				if len(opts.FieldFilters) > 0 && !matchesFieldFilters(event.Object, opts.FieldFilters) {
					continue
				}

				select {
				case outCh <- event:
				case <-stopCh:
					return
				}
			}
		}
	}()

	var stopOnce sync.Once
	stopFunc := func() {
		stopOnce.Do(func() { close(stopCh) })
	}

	return outCh, stopFunc, nil
}

func matchesFieldFilters(obj client.Object, filters map[string]string) bool {
	data, err := json.Marshal(obj)
	if err != nil {
		return false
	}
	var objMap map[string]any
	if err := json.Unmarshal(data, &objMap); err != nil {
		return false
	}
	for path, expected := range filters {
		if fieldValueFromMap(objMap, path) != expected {
			return false
		}
	}
	return true
}

func fieldValueFromMap(m map[string]any, path string) string {
	parts := strings.Split(path, ".")
	current := any(m)
	for _, part := range parts {
		cm, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = cm[part]
	}
	s, _ := current.(string)
	return s
}

// Close shuts down the broadcaster (and its change-stream partition readers)
// associated with this store. Safe to call multiple times.
func (s *SpannerStore) Close() error {
	if b, ok := s.broadcaster.(interface{ Close() error }); ok {
		return b.Close()
	}
	return nil
}

var _ storage.ResourceStore = (*SpannerStore)(nil)
