package placement

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openshift-online/gecko/controllers/util/logger"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// testLogger creates a logger for tests.
func testLogger(t *testing.T) logger.Logger {
	t.Helper()
	log, err := logger.NewLogger(logger.Config{
		Level:     "debug",
		Format:    logger.FormatText,
		Output:    "stdout",
		Component: "test",
		Version:   "test",
	})
	require.NoError(t, err)
	return log
}

// mockStatusWriter is a minimal SubResourceWriter for status updates.
type mockStatusWriter struct {
	updateErr    error
	updateCalled bool
}

func (m *mockStatusWriter) Update(_ context.Context, _ client.Object, _ ...client.SubResourceUpdateOption) error {
	m.updateCalled = true
	return m.updateErr
}
func (m *mockStatusWriter) Create(_ context.Context, _ client.Object, _ client.Object, _ ...client.SubResourceCreateOption) error {
	return nil
}
func (m *mockStatusWriter) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	return nil
}
func (m *mockStatusWriter) Apply(_ context.Context, _ runtime.ApplyConfiguration, _ ...client.SubResourceApplyOption) error {
	return nil
}

// mockStoreClient is a minimal client.Client that captures Update/Status calls.
type mockStoreClient struct {
	cluster      *privatev1.Cluster
	getErr       error
	updateErr    error
	statusWriter *mockStatusWriter
	updateCalled bool
}

func (m *mockStoreClient) Get(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	if m.getErr != nil {
		return m.getErr
	}
	if m.cluster == nil {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "cluster"}, "")
	}
	c, ok := obj.(*privatev1.Cluster)
	if !ok {
		return fmt.Errorf("unexpected type %T", obj)
	}
	*c = *m.cluster
	return nil
}

func (m *mockStoreClient) Update(_ context.Context, _ client.Object, _ ...client.UpdateOption) error {
	m.updateCalled = true
	return m.updateErr
}

func (m *mockStoreClient) Status() client.SubResourceWriter {
	if m.statusWriter == nil {
		m.statusWriter = &mockStatusWriter{}
	}
	return m.statusWriter
}

func (m *mockStoreClient) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return nil
}
func (m *mockStoreClient) Create(_ context.Context, _ client.Object, _ ...client.CreateOption) error {
	return nil
}
func (m *mockStoreClient) Delete(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
	return nil
}
func (m *mockStoreClient) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
	return nil
}
func (m *mockStoreClient) DeleteAllOf(_ context.Context, _ client.Object, _ ...client.DeleteAllOfOption) error {
	return nil
}
func (m *mockStoreClient) Apply(_ context.Context, _ runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
	return nil
}
func (m *mockStoreClient) SubResource(_ string) client.SubResourceClient { return nil }
func (m *mockStoreClient) Scheme() *runtime.Scheme                       { return nil }
func (m *mockStoreClient) RESTMapper() meta.RESTMapper                   { return nil }
func (m *mockStoreClient) GroupVersionKindFor(_ runtime.Object) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, nil
}
func (m *mockStoreClient) IsObjectNamespaced(_ runtime.Object) (bool, error) { return false, nil }

// buildCluster builds a Cluster for use in tests.
func buildCluster(id string, placed bool) *privatev1.Cluster {
	c := &privatev1.Cluster{}
	c.SetName(id)
	c.SetNamespace("hyperfleet")
	if placed {
		c.Status.PlacementResult = &privatev1.PlacementResult{
			ManagementClusterName: "mc-us-c1",
			BaseDomain:            "hc-us-central1-abc.example.com",
		}
	}
	return c
}

// mockSelector is a minimal Selector for reconciler tests.
type mockSelector struct {
	mc     string
	domain string
	err    error
}

func (m *mockSelector) Select(_ context.Context) (string, string, error) {
	return m.mc, m.domain, m.err
}

// workingSelector returns a Selector that always succeeds.
func workingSelector() Selector {
	return &mockSelector{mc: "mc-us-c1", domain: "hc-us-central1-abc.example.com"}
}

// failingSelector returns a Selector whose Select() always errors.
func failingSelector(errMsg string) Selector {
	return &mockSelector{err: fmt.Errorf("%s", errMsg)}
}

func TestReconciler(t *testing.T) {
	tests := []struct {
		name               string
		clusterID          string
		cluster            *privatev1.Cluster // nil → NotFound
		selector           Selector
		expectUpdate       bool
		expectStatusUpdate bool
		expectedResult     reconcile.Result
		expectError        bool
	}{
		{
			name:               "happy path: selects MC and domain, updates status",
			clusterID:          "cluster-1",
			cluster:            buildCluster("cluster-1", false),
			selector:           workingSelector(),
			expectUpdate:       false,
			expectStatusUpdate: true,
			expectedResult:     reconcile.Result{RequeueAfter: requeueStable},
		},
		{
			name:               "already placed: no update, empty result",
			clusterID:          "cluster-2",
			cluster:            buildCluster("cluster-2", true),
			selector:           workingSelector(),
			expectUpdate:       false,
			expectStatusUpdate: false,
			expectedResult:     reconcile.Result{},
		},
		{
			name:               "cluster not found: return empty result, no error",
			clusterID:          "cluster-missing",
			cluster:            nil, // → NotFoundError
			selector:           workingSelector(),
			expectUpdate:       false,
			expectStatusUpdate: false,
			expectedResult:     reconcile.Result{},
			expectError:        false,
		},
		{
			name:               "selector error: return error",
			clusterID:          "cluster-4",
			cluster:            buildCluster("cluster-4", false),
			selector:           failingSelector("no candidates available"),
			expectUpdate:       false,
			expectStatusUpdate: false,
			expectError:        true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storeClient := &mockStoreClient{cluster: tc.cluster, statusWriter: &mockStatusWriter{}}

			reconciler := &Reconciler{
				client:   storeClient,
				selector: tc.selector,
				log:      testLogger(t),
			}

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: "hyperfleet",
					Name:      tc.clusterID,
				},
			}
			result, err := reconciler.Reconcile(context.Background(), req)

			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tc.expectedResult, result)
			require.Equal(t, tc.expectUpdate, storeClient.updateCalled, "Update called mismatch")
			require.Equal(t, tc.expectStatusUpdate, storeClient.statusWriter.updateCalled, "Status().Update() called mismatch")
		})
	}
}

func TestSetCondition(t *testing.T) {
	t.Run("appends new condition", func(t *testing.T) {
		var conds []metav1.Condition
		meta.SetStatusCondition(&conds, metav1.Condition{Type: "Applied", Status: metav1.ConditionTrue, Reason: "Test"})
		require.Len(t, conds, 1)
		require.Equal(t, "Applied", conds[0].Type)
		require.Equal(t, metav1.ConditionTrue, conds[0].Status)
		require.False(t, conds[0].LastTransitionTime.IsZero())
	})

	t.Run("preserves LastTransitionTime on same status update", func(t *testing.T) {
		transitioned := metav1.Now()
		conds := []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionTrue, LastTransitionTime: transitioned, Reason: "Test"},
		}
		meta.SetStatusCondition(&conds, metav1.Condition{Type: "Applied", Status: metav1.ConditionTrue, Reason: "Test"})
		require.Len(t, conds, 1)
		require.Equal(t, transitioned, conds[0].LastTransitionTime)
	})

	t.Run("updates LastTransitionTime on status change", func(t *testing.T) {
		old := metav1.Now()
		conds := []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionFalse, LastTransitionTime: old, Reason: "Test"},
		}
		meta.SetStatusCondition(&conds, metav1.Condition{Type: "Applied", Status: metav1.ConditionTrue, Reason: "Test"})
		require.Len(t, conds, 1)
		require.False(t, conds[0].LastTransitionTime.IsZero())
	})
}
