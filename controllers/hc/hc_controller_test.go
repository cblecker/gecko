package hc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	"github.com/openshift-online/gecko/controllers/client/transport"
	"github.com/openshift-online/gecko/controllers/client/transport/mock"
	"github.com/openshift-online/gecko/controllers/hc"
	"github.com/openshift-online/gecko/controllers/util/constants"
	"github.com/openshift-online/gecko/controllers/util/logger"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testLogger(t *testing.T) logger.Logger {
	t.Helper()
	log, err := logger.NewLogger(logger.Config{
		Level:     "error",
		Format:    "text",
		Output:    "stderr",
		Component: "test",
	})
	require.NoError(t, err)
	return log
}

// mockStatusWriter captures Status().Update calls and can return a configured error.
type mockStatusWriter struct {
	called    bool
	updateErr error
	captured  client.Object
}

func (m *mockStatusWriter) Update(_ context.Context, obj client.Object, _ ...client.SubResourceUpdateOption) error {
	m.called = true
	m.captured = obj
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

// mockStoreClient is a minimal client.Client backed by a fixed Cluster.
type mockStoreClient struct {
	cluster      *privatev1.Cluster
	getErr       error
	statusWriter *mockStatusWriter
	updateCalled bool
	updateErr    error
	updated      client.Object
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

func (m *mockStoreClient) Status() client.SubResourceWriter { return m.statusWriter }

func (m *mockStoreClient) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return nil
}
func (m *mockStoreClient) Create(_ context.Context, _ client.Object, _ ...client.CreateOption) error {
	return nil
}
func (m *mockStoreClient) Delete(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
	return nil
}
func (m *mockStoreClient) Update(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
	m.updateCalled = true
	m.updated = obj
	return m.updateErr
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

// errTransport is a transport.Client that returns configurable errors.
type errTransport struct {
	applyErr    error
	applyResult *transport.Status
	deleteErr   error
}

func (e *errTransport) Apply(_ context.Context, _, _ string, _ [][]byte) (*transport.Status, error) {
	return e.applyResult, e.applyErr
}
func (e *errTransport) GetStatus(_ context.Context, _, _ string) (*transport.Status, error) {
	return nil, nil
}
func (e *errTransport) Delete(_ context.Context, _, _ string) error { return e.deleteErr }
func (e *errTransport) GetDeleteStatus(_ context.Context, _, _ string) (*transport.DeleteStatus, error) {
	// Return ApplyDesiresCount=1 to trigger Delete() call.
	return &transport.DeleteStatus{AllSuccessful: false, TotalCount: 0, PendingCount: 0, ApplyDesiresCount: 1}, nil
}
func (e *errTransport) CleanupDeleteDesires(_ context.Context, _, _ string) error { return nil }

// clusterReq returns a reconcile.Request for the given cluster name.
func clusterReq(name string) reconcile.Request {
	return reconcile.Request{
		NamespacedName: client.ObjectKey{Namespace: "hyperfleet", Name: name},
	}
}

// conflictErr returns a Kubernetes conflict error.
func conflictErr() error {
	return apierrors.NewConflict(schema.GroupResource{Resource: "clusters"}, "test", fmt.Errorf("conflict"))
}

// buildReadyCluster creates a Cluster with placement and VR results set.
func buildReadyCluster(clusterID, version string) *privatev1.Cluster {
	c := &privatev1.Cluster{}
	c.SetName(clusterID)
	c.SetNamespace("hyperfleet")
	c.SetGeneration(2)
	c.SetFinalizers([]string{constants.FinalizerCluster})
	c.Spec = privatev1.ClusterSpec{
		InfraID: "infra-xyz",
		Release: privatev1.ReleaseSpec{Version: version},
		Platform: privatev1.ClusterPlatformSpec{
			Type: "GCP",
			GCP: &privatev1.GCPClusterPlatform{
				ProjectID: "my-project",
				Region:    "us-central1",
				Network:   "my-vpc",
				Subnet:    "my-subnet",
			},
		},
	}
	c.Status = privatev1.ClusterStatus{
		PlacementResult: &privatev1.PlacementResult{
			ManagementClusterName: "mc-cluster-1",
			BaseDomain:            "example.com",
		},
		VersionResolution: &privatev1.VersionResolutionResult{
			ReleaseImage:      "quay.io/openshift-release-dev/ocp-release:4.15.0-x86_64",
			ReleaseVersion:    version,
			CincinnatiChannel: "stable-4.15",
		},
	}
	return c
}

// buildReconciler wires up an hc.Reconciler backed by the given store and transport.
// getErr is injected into the cluster Get call; statusErr is returned by Status().Update;
// updateErr is returned by Update (used for finalizer add/remove).
func buildReconciler(
	t *testing.T,
	cluster *privatev1.Cluster,
	getErr error,
	tr transport.Client,
	statusErr error,
	opts ...func(*mockStoreClient),
) (*hc.Reconciler, *mockStoreClient) {
	t.Helper()
	storeClient := &mockStoreClient{
		cluster:      cluster,
		getErr:       getErr,
		statusWriter: &mockStatusWriter{updateErr: statusErr},
	}
	for _, o := range opts {
		o(storeClient)
	}
	return hc.New(tr, testLogger(t), storeClient), storeClient
}

// ---------------------------------------------------------------------------
// Test cases – early exits
// ---------------------------------------------------------------------------

// TestReconcile_ClusterNotFound verifies that a 404 on Get returns an empty Result with no error.
func TestReconcile_ClusterNotFound(t *testing.T) {
	tr := mock.New()
	r, _ := buildReconciler(t, nil, nil, tr, nil) // nil cluster → NotFound

	result, err := r.Reconcile(context.Background(), clusterReq("cluster-missing"))
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)
	require.Empty(t, tr.ApplyCalls)
}

// TestReconcile_ClusterGetError verifies that a non-404 error from Get is propagated.
func TestReconcile_ClusterGetError(t *testing.T) {
	tr := mock.New()
	r, _ := buildReconciler(t, nil, fmt.Errorf("etcd timeout"), tr, nil)

	_, err := r.Reconcile(context.Background(), clusterReq("cluster-abc"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "get cluster")
	require.Empty(t, tr.ApplyCalls)
}

// TestReconcile_DependenciesNotReady_NoPlacement verifies requeue when placement is missing.
func TestReconcile_DependenciesNotReady_NoPlacement(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := &privatev1.Cluster{}
	cluster.SetName(clusterID)
	cluster.SetNamespace("hyperfleet")
	cluster.SetFinalizers([]string{constants.FinalizerCluster})
	cluster.Status.VersionResolution = &privatev1.VersionResolutionResult{
		ReleaseImage:   "quay.io/openshift-release-dev/ocp-release:4.15.0-x86_64",
		ReleaseVersion: "4.15.0",
	}
	// PlacementResult is nil.

	tr := mock.New()
	r, _ := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter, "should requeue while placement is not ready")
	require.Empty(t, tr.ApplyCalls)
}

// TestReconcile_PlacementNotReady_StatusUpdateConflict verifies that a conflict error on the
// waiting-conditions Status.Update is silently swallowed.
func TestReconcile_PlacementNotReady_StatusUpdateConflict(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := &privatev1.Cluster{}
	cluster.SetName(clusterID)
	cluster.SetNamespace("hyperfleet")
	cluster.SetFinalizers([]string{constants.FinalizerCluster})
	// No conditions → setWaitingConditions adds them → returns true → Status.Update called.

	tr := mock.New()
	r, storeClient := buildReconciler(t, cluster, nil, tr, conflictErr())

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter)
	require.True(t, storeClient.statusWriter.called, "expected Status.Update to be called")
	require.Empty(t, tr.ApplyCalls)
}

// TestReconcile_PlacementNotReady_StatusUpdateError verifies that a non-conflict error on
// Status.Update is propagated.
func TestReconcile_PlacementNotReady_StatusUpdateError(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := &privatev1.Cluster{}
	cluster.SetName(clusterID)
	cluster.SetNamespace("hyperfleet")
	cluster.SetFinalizers([]string{constants.FinalizerCluster})

	tr := mock.New()
	r, _ := buildReconciler(t, cluster, nil, tr, fmt.Errorf("server error"))

	_, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.Error(t, err)
	require.Contains(t, err.Error(), "update cluster status")
}

// TestReconcile_VRNil_SetsWaitingConditions verifies that a nil VersionResolution sets
// waiting conditions and returns an empty result.
func TestReconcile_VRNil_SetsWaitingConditions(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := &privatev1.Cluster{}
	cluster.SetName(clusterID)
	cluster.SetNamespace("hyperfleet")
	cluster.SetFinalizers([]string{constants.FinalizerCluster})
	cluster.Status.PlacementResult = &privatev1.PlacementResult{
		ManagementClusterName: "mc-cluster-1",
		BaseDomain:            "example.com",
	}
	// VersionResolution is nil.

	tr := mock.New()
	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter, "should requeue while VR is not ready")
	require.True(t, storeClient.statusWriter.called)
	require.Empty(t, tr.ApplyCalls)
}

// TestReconcile_VRNil_StatusUpdateConflict verifies that a conflict on Status.Update when
// VR is nil is silently swallowed.
func TestReconcile_VRNil_StatusUpdateConflict(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := &privatev1.Cluster{}
	cluster.SetName(clusterID)
	cluster.SetNamespace("hyperfleet")
	cluster.SetFinalizers([]string{constants.FinalizerCluster})
	cluster.Status.PlacementResult = &privatev1.PlacementResult{ManagementClusterName: "mc-1"}

	tr := mock.New()
	r, storeClient := buildReconciler(t, cluster, nil, tr, conflictErr())

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter)
	require.True(t, storeClient.statusWriter.called)
}

// TestReconcile_VRNil_StatusUpdateError verifies that a non-conflict error on Status.Update
// when VR is nil is propagated.
func TestReconcile_VRNil_StatusUpdateError(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := &privatev1.Cluster{}
	cluster.SetName(clusterID)
	cluster.SetNamespace("hyperfleet")
	cluster.SetFinalizers([]string{constants.FinalizerCluster})
	cluster.Status.PlacementResult = &privatev1.PlacementResult{ManagementClusterName: "mc-1"}

	tr := mock.New()
	r, _ := buildReconciler(t, cluster, nil, tr, fmt.Errorf("write error"))

	_, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.Error(t, err)
	require.Contains(t, err.Error(), "update cluster status")
}

// TestReconcile_DependenciesNotReady_VRVersionMismatch verifies requeue when VR version
// doesn't match the spec version.
func TestReconcile_DependenciesNotReady_VRVersionMismatch(t *testing.T) {
	clusterID := "cluster-abc"
	// Cluster wants 4.15.0 but VR resolved 4.14.9.
	cluster := buildReadyCluster(clusterID, "4.14.9")
	cluster.Spec.Release = privatev1.ReleaseSpec{Version: "4.15.0"}

	tr := mock.New()
	r, _ := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter, "should requeue while VR version mismatches")
	require.Empty(t, tr.ApplyCalls)
}

// TestReconcile_VRVersionMismatch_StatusUpdateConflict verifies conflict is swallowed
// on the version-mismatch waiting condition update.
func TestReconcile_VRVersionMismatch_StatusUpdateConflict(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := buildReadyCluster(clusterID, "4.14.9")
	cluster.Spec.Release = privatev1.ReleaseSpec{Version: "4.15.0"}

	tr := mock.New()
	r, storeClient := buildReconciler(t, cluster, nil, tr, conflictErr())

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter)
	require.True(t, storeClient.statusWriter.called)
}

// TestReconcile_VRVersionMismatch_StatusUpdateError verifies that a non-conflict error
// on Status.Update when versions mismatch is propagated.
func TestReconcile_VRVersionMismatch_StatusUpdateError(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := buildReadyCluster(clusterID, "4.14.9")
	cluster.Spec.Release = privatev1.ReleaseSpec{Version: "4.15.0"}

	tr := mock.New()
	r, _ := buildReconciler(t, cluster, nil, tr, fmt.Errorf("write error"))

	_, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.Error(t, err)
	require.Contains(t, err.Error(), "update cluster status")
}

// ---------------------------------------------------------------------------
// Test cases – manifest build and transport
// ---------------------------------------------------------------------------

// TestReconcile_ManifestBuildError verifies that a manifest.Build failure is propagated.
// A nil GCP spec leaves required fields empty, triggering validation inside Build.
func TestReconcile_ManifestBuildError(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := buildReadyCluster(clusterID, "4.15.0")
	cluster.Spec.Platform.GCP = nil // gcpProjectID="" → Build fails validation

	tr := mock.New()
	r, _ := buildReconciler(t, cluster, nil, tr, nil)

	_, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.Error(t, err)
	require.Contains(t, err.Error(), "build manifests")
	require.Empty(t, tr.ApplyCalls)
}

// TestReconcile_TransportApplyError verifies that a transport Apply failure is propagated.
func TestReconcile_TransportApplyError(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := buildReadyCluster(clusterID, "4.15.0")

	tr := &errTransport{applyErr: fmt.Errorf("maestro unavailable")}
	r, _ := buildReconciler(t, cluster, nil, tr, nil)

	_, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.Error(t, err)
	require.Contains(t, err.Error(), "apply resources")
}

// TestReconcile_MWStatusNil_RequeuesPending verifies that when Apply returns nil status
// (resources not yet processed), both conditions are set to False and the reconciler
// requeues with the pending interval.
func TestReconcile_MWStatusNil_RequeuesPending(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := buildReadyCluster(clusterID, "4.15.0")

	// Apply returns nil status to simulate the resources not yet having a status.
	tr := &errTransport{}
	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter)
	require.True(t, storeClient.statusWriter.called)
}

// ---------------------------------------------------------------------------
// Test cases – happy path and condition-driven requeue
// ---------------------------------------------------------------------------

// TestReconcile_HappyPath verifies the full reconcile path when all dependencies are ready
// and the resources have been applied successfully.
func TestReconcile_HappyPath(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"

	cluster := buildReadyCluster(clusterID, "4.15.0")

	tr := mock.New()
	hcKey := fmt.Sprintf("hypershift.openshift.io/v1beta1/hostedclusters/clusters-%s/%s", clusterID, clusterID)
	tr.StatusOverrides[mcName+"/"+clusterID] = &transport.Status{
		Conditions: []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionTrue, Reason: "AppliedSuccessfully", LastTransitionTime: metav1.Now()},
		},
		ResourceStatuses: map[string]map[string]string{
			hcKey: {"availableCondition": "True", "degradedCondition": "False"},
		},
	}

	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, result.RequeueAfter)
	require.Len(t, tr.ApplyCalls, 1)
	require.Equal(t, mcName, tr.ApplyCalls[0].TargetCluster)
	require.Equal(t, clusterID, tr.ApplyCalls[0].ClusterID)
	require.True(t, storeClient.statusWriter.called, "expected Status().Update to be called")
}

// TestReconcile_EndpointAccessPropagated verifies that the EndpointAccess value from the
// cluster spec is passed through to the HostedCluster manifest.
func TestReconcile_EndpointAccessPropagated(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"

	cluster := buildReadyCluster(clusterID, "4.15.0")
	cluster.Spec.Platform.GCP.EndpointAccess = "PublicAndPrivate"

	tr := mock.New()
	hcKey := fmt.Sprintf("hypershift.openshift.io/v1beta1/hostedclusters/clusters-%s/%s", clusterID, clusterID)
	tr.StatusOverrides[mcName+"/"+clusterID] = &transport.Status{
		Conditions: []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionTrue, Reason: "AppliedSuccessfully", LastTransitionTime: metav1.Now()},
		},
		ResourceStatuses: map[string]map[string]string{
			hcKey: {"availableCondition": "True"},
		},
	}

	r, _ := buildReconciler(t, cluster, nil, tr, nil)

	_, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Len(t, tr.ApplyCalls, 1)

	// The HostedCluster manifest is the 4th manifest (index 3).
	var obj map[string]any
	require.NoError(t, json.Unmarshal(tr.ApplyCalls[0].Manifests[3], &obj))
	spec := obj["spec"].(map[string]any)
	platform := spec["platform"].(map[string]any)
	gcp := platform["gcp"].(map[string]any)
	require.Equal(t, "PublicAndPrivate", gcp["endpointAccess"], "EndpointAccess from cluster spec should be propagated to the HostedCluster manifest")
}

// TestReconcile_HCFeedback_SetsHostedClusterResult verifies that controlPlaneEndpoint and
// version fields from HC status feedback are written to cluster.Status.HostedClusterResult.
func TestReconcile_HCFeedback_SetsHostedClusterResult(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"

	cluster := buildReadyCluster(clusterID, "4.15.0")

	tr := mock.New()
	hcKey := fmt.Sprintf("hypershift.openshift.io/v1beta1/hostedclusters/clusters-%s/%s", clusterID, clusterID)
	tr.StatusOverrides[mcName+"/"+clusterID] = &transport.Status{
		Conditions: []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionTrue, Reason: "AppliedSuccessfully", LastTransitionTime: metav1.Now()},
		},
		ResourceStatuses: map[string]map[string]string{
			hcKey: {
				"availableCondition":   "True",
				"controlPlaneEndpoint": "api.my-cluster-user.example.com",
				"version":              "4.15.0",
			},
		},
	}

	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, result.RequeueAfter)

	require.True(t, storeClient.statusWriter.called)
	captured := storeClient.statusWriter.captured.(*privatev1.Cluster)
	require.NotNil(t, captured.Status.HostedClusterResult)
	require.Equal(t, "api.my-cluster-user.example.com", captured.Status.HostedClusterResult.APIEndpoint)
	require.Equal(t, "4.15.0", captured.Status.HostedClusterResult.Version)
}

// TestReconcile_CreatedByAnnotationPropagated verifies that the created-by annotation
// from the cluster's metadata is passed through to the manifest input.
func TestReconcile_CreatedByAnnotationPropagated(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"

	cluster := buildReadyCluster(clusterID, "4.15.0")
	cluster.SetAnnotations(map[string]string{
		constants.AnnotationCreatedBy: "user@example.com",
	})

	tr := mock.New()
	hcKey := fmt.Sprintf("hypershift.openshift.io/v1beta1/hostedclusters/clusters-%s/%s", clusterID, clusterID)
	tr.StatusOverrides[mcName+"/"+clusterID] = &transport.Status{
		Conditions: []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionTrue, Reason: "AppliedSuccessfully", LastTransitionTime: metav1.Now()},
		},
		ResourceStatuses: map[string]map[string]string{
			hcKey: {"availableCondition": "True"},
		},
	}

	r, _ := buildReconciler(t, cluster, nil, tr, nil)

	_, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Len(t, tr.ApplyCalls, 1)

	// The RBAC job is the last manifest; find the Job manifest by kind.
	var jobFound bool
	for _, m := range tr.ApplyCalls[0].Manifests {
		var obj map[string]any
		require.NoError(t, json.Unmarshal(m, &obj))
		if obj["kind"] == "Job" {
			jobFound = true
			// The job's script should contain the created-by email.
			spec := obj["spec"].(map[string]any)
			template := spec["template"].(map[string]any)
			podSpec := template["spec"].(map[string]any)
			containers := podSpec["containers"].([]any)
			container := containers[0].(map[string]any)
			command := container["command"].([]any)
			script := command[len(command)-1].(string)
			require.Contains(t, script, "user@example.com", "RBAC job script should contain the created-by email")
			break
		}
	}
	require.True(t, jobFound, "expected a Job manifest in the apply call")
}

// TestReconcile_MWNoAppliedCondition_RequeuesPending verifies that when the resources
// status has no "Applied" condition, the reconciler requeues with the pending interval.
// This also exercises the mwCondition default return path.
func TestReconcile_MWNoAppliedCondition_RequeuesPending(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"

	cluster := buildReadyCluster(clusterID, "4.15.0")

	tr := mock.New()
	tr.StatusOverrides[mcName+"/"+clusterID] = &transport.Status{} // no conditions at all

	r, _ := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter)
}

// TestReconcile_MWNotApplied_RequeuesPending verifies that when the Applied condition is
// explicitly False, the reconciler requeues with the pending interval.
func TestReconcile_MWNotApplied_RequeuesPending(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"

	cluster := buildReadyCluster(clusterID, "4.15.0")

	tr := mock.New()
	tr.StatusOverrides[mcName+"/"+clusterID] = &transport.Status{
		Conditions: []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionFalse, Reason: "ApplyFailed", LastTransitionTime: metav1.Now()},
		},
	}

	r, _ := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter)
}

// TestReconcile_ResourcesApplied_NotYetAvailable_RequeuesPending verifies that when
// resources are applied but HostedClusterAvailable is False, the reconciler keeps
// polling at the pending interval instead of switching to the stable 5m interval.
func TestReconcile_ResourcesApplied_NotYetAvailable_RequeuesPending(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"

	cluster := buildReadyCluster(clusterID, "4.15.0")

	tr := mock.New()
	tr.StatusOverrides[mcName+"/"+clusterID] = &transport.Status{
		Conditions: []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionTrue, Reason: "AppliedSuccessfully", LastTransitionTime: metav1.Now()},
		},
		// No HC feedback → HostedClusterAvailable stays False
	}

	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter, "should requeue at pending interval while HostedClusterAvailable is False")
	require.True(t, storeClient.statusWriter.called)

	// Verify the conditions were set correctly
	captured := storeClient.statusWriter.captured.(*privatev1.Cluster)
	ra := meta.FindStatusCondition(captured.Status.Conditions, "ResourcesApplied")
	require.NotNil(t, ra)
	require.Equal(t, metav1.ConditionTrue, ra.Status)
	hca := meta.FindStatusCondition(captured.Status.Conditions, "HostedClusterAvailable")
	require.NotNil(t, hca)
	require.Equal(t, metav1.ConditionFalse, hca.Status)
}

// TestReconcile_ApplyConditions_Idempotent verifies that when the cluster's conditions
// already match the current applied status, applyStatusConditions returns false and
// Status.Update is not called.
func TestReconcile_ApplyConditions_Idempotent(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"

	cluster := buildReadyCluster(clusterID, "4.15.0")
	// Pre-populate conditions to exactly match what applyStatusConditions would set,
	// including ObservedGeneration (cluster.Generation = 2).
	cluster.Status.Conditions = []metav1.Condition{
		{Type: "ResourcesApplied", Status: metav1.ConditionTrue, Reason: "AppliedSuccessfully", Message: "", ObservedGeneration: 2},
		{Type: "HostedClusterAvailable", Status: metav1.ConditionFalse, Reason: "HostedClusterNotAvailable", Message: "", ObservedGeneration: 2},
		{Type: "ApiCertificateReady", Status: metav1.ConditionFalse, Reason: "CertificateNotReady", Message: "", ObservedGeneration: 2},
	}

	tr := mock.New()
	tr.StatusOverrides[mcName+"/"+clusterID] = &transport.Status{
		Conditions: []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionTrue, Reason: "AppliedSuccessfully"},
		},
		// No ResourceStatuses → no HC feedback → availableStatus stays "False"
	}

	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter) // HostedClusterAvailable=False → requeuePending
	require.False(t, storeClient.statusWriter.called, "Status.Update should not be called when conditions are unchanged")
}

// TestReconcile_StatusUpdateConflict_ReturnsNoError verifies that a conflict error on
// Status.Update after applyStatusConditions is silently swallowed.
func TestReconcile_StatusUpdateConflict_ReturnsNoError(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"

	cluster := buildReadyCluster(clusterID, "4.15.0")
	// No prior conditions → applyStatusConditions will add them → returns true → Update called.

	tr := mock.New()
	tr.StatusOverrides[mcName+"/"+clusterID] = &transport.Status{
		Conditions: []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionTrue, Reason: "AppliedSuccessfully"},
		},
	}

	r, storeClient := buildReconciler(t, cluster, nil, tr, conflictErr())

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter) // conflict → returns immediately
	require.True(t, storeClient.statusWriter.called)
}

// TestReconcile_WithServiceAccountsRef verifies that WIF service account emails are
// extracted from ServiceAccountsRef and passed through to the manifest build.
func TestReconcile_WithServiceAccountsRef(t *testing.T) {
	clusterID := "cluster-wif"
	mcName := "mc-cluster-1"

	cluster := buildReadyCluster(clusterID, "4.15.0")
	cluster.Spec.Platform.GCP.WorkloadIdentity = privatev1.WorkloadIdentitySpec{
		ProjectNumber: "123456789",
		PoolID:        "my-pool",
		ProviderID:    "my-provider",
		ServiceAccountsRef: &privatev1.ServiceAccountsRef{
			NodePoolEmail:        "nodepool@project.iam.gserviceaccount.com",
			ControlPlaneEmail:    "cp@project.iam.gserviceaccount.com",
			CloudControllerEmail: "cc@project.iam.gserviceaccount.com",
			StorageEmail:         "storage@project.iam.gserviceaccount.com",
			ImageRegistryEmail:   "registry@project.iam.gserviceaccount.com",
			NetworkEmail:         "network@project.iam.gserviceaccount.com",
		},
	}

	tr := mock.New()
	tr.StatusOverrides[mcName+"/"+clusterID] = &transport.Status{
		Conditions: []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionTrue, Reason: "AppliedSuccessfully"},
		},
	}

	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter) // ResourcesApplied=True but HostedClusterAvailable=False → requeuePending
	require.Len(t, tr.ApplyCalls, 1)
	require.True(t, storeClient.statusWriter.called)
}

// TestReconcile_StatusUpdateError_ReturnsError verifies that a non-conflict error on
// Status.Update after applyStatusConditions is propagated.
func TestReconcile_StatusUpdateError_ReturnsError(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"

	cluster := buildReadyCluster(clusterID, "4.15.0")

	tr := mock.New()
	tr.StatusOverrides[mcName+"/"+clusterID] = &transport.Status{
		Conditions: []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionTrue, Reason: "AppliedSuccessfully"},
		},
	}

	r, storeClient := buildReconciler(t, cluster, nil, tr, fmt.Errorf("server error"))

	_, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.Error(t, err)
	require.Contains(t, err.Error(), "update cluster status")
	require.True(t, storeClient.statusWriter.called)
}

// TestReconcile_StaleStatus_RequeuesPending verifies that when the transport
// reports stale status, the reconciler requeues with the pending interval even
// though resources are applied, and does not write stale conditions.
func TestReconcile_StaleStatus_RequeuesPending(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"

	cluster := buildReadyCluster(clusterID, "4.15.0")

	tr := mock.New()
	tr.StatusOverrides[mcName+"/"+clusterID] = &transport.Status{
		Conditions: []metav1.Condition{
			{Type: "Applied", Status: metav1.ConditionTrue, Reason: "AppliedSuccessfully"},
		},
		Stale: true,
	}

	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter)

	// Verify that stale status was not written to the cluster conditions.
	require.Nil(t, storeClient.statusWriter.captured, "status should not be updated when status is stale")
}

// ---------------------------------------------------------------------------
// Test cases – finalizer management
// ---------------------------------------------------------------------------

// TestReconcile_AddsFinalizer verifies that a cluster without the finalizer gets it added
// and the reconciler returns immediately to re-reconcile.
func TestReconcile_AddsFinalizer(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := buildReadyCluster(clusterID, "4.15.0")
	cluster.SetFinalizers(nil) // no finalizer yet

	tr := mock.New()
	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)
	require.True(t, storeClient.updateCalled, "expected Update to be called to add finalizer")
	require.Empty(t, tr.ApplyCalls, "should not Apply before finalizer is persisted")

	updated := storeClient.updated.(*privatev1.Cluster)
	require.Contains(t, updated.Finalizers, constants.FinalizerCluster)
}

// TestReconcile_AddFinalizer_UpdateError verifies that an Update error when adding the
// finalizer is propagated.
func TestReconcile_AddFinalizer_UpdateError(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := buildReadyCluster(clusterID, "4.15.0")
	cluster.SetFinalizers(nil) // no finalizer yet

	tr := mock.New()
	r, _ := buildReconciler(t, cluster, nil, tr, nil, func(m *mockStoreClient) {
		m.updateErr = fmt.Errorf("etcd write error")
	})

	_, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.Error(t, err)
	require.Contains(t, err.Error(), "add finalizer")
}

// ---------------------------------------------------------------------------
// Test cases – deletion
// ---------------------------------------------------------------------------

// TestReconcile_Deletion_HappyPath verifies the async deletion flow:
// 1. First reconcile: calls transport.Delete, requeues
// 2. Second reconcile: polls GetDeleteStatus (returns AllSuccessful=true), cleans up, removes finalizer
func TestReconcile_Deletion_HappyPath(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"
	cluster := buildReadyCluster(clusterID, "4.15.0")
	cluster.Status.Conditions = append(cluster.Status.Conditions, metav1.Condition{
		Type: "ResourcesApplied", Status: metav1.ConditionTrue, Reason: "Applied",
	})
	now := metav1.Now()
	cluster.SetDeletionTimestamp(&now)

	tr := mock.New()
	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	// Simulate ApplyDesires exist (deletion not started).
	key := mcName + "/" + clusterID
	tr.DeleteStatusOverrides[key] = &transport.DeleteStatus{
		AllSuccessful:     false,
		TotalCount:        0,
		PendingCount:      0,
		ApplyDesiresCount: 5,
	}

	// First reconcile: initiate deletion.
	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter, "should requeue after initiating delete")
	require.Len(t, tr.DeleteCalls, 1)
	require.Equal(t, mcName, tr.DeleteCalls[0].TargetCluster)
	require.Equal(t, clusterID, tr.DeleteCalls[0].ClusterID)
	require.False(t, storeClient.updateCalled, "finalizer not removed yet")

	// Simulate kube-applier-gcp completing deletion.
	tr.DeleteStatusOverrides[key] = &transport.DeleteStatus{AllSuccessful: true, TotalCount: 5, PendingCount: 0, ApplyDesiresCount: 0}

	// Second reconcile: check status, cleanup, remove finalizer.
	storeClient.updateCalled = false // reset
	result, err = r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)
	require.Len(t, tr.CleanupDeleteDesiresCalls, 1, "should cleanup DeleteDesires")
	require.Equal(t, mcName, tr.CleanupDeleteDesiresCalls[0].TargetCluster)
	require.Equal(t, clusterID, tr.CleanupDeleteDesiresCalls[0].ClusterID)

	require.True(t, storeClient.updateCalled, "expected Update to remove finalizer")
	updated := storeClient.updated.(*privatev1.Cluster)
	require.NotContains(t, updated.Finalizers, constants.FinalizerCluster)
}

// TestReconcile_Deletion_NoPlacement verifies that when the cluster has no placement
// (no resources on any MC), transport.Delete is NOT called but the finalizer is still removed.
func TestReconcile_Deletion_NoPlacement(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := &privatev1.Cluster{}
	cluster.SetName(clusterID)
	cluster.SetNamespace("hyperfleet")
	cluster.SetFinalizers([]string{constants.FinalizerCluster})
	now := metav1.Now()
	cluster.SetDeletionTimestamp(&now)
	// No PlacementResult.

	tr := mock.New()
	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)

	require.Empty(t, tr.DeleteCalls, "should not call Delete without placement")
	require.True(t, storeClient.updateCalled, "expected Update to remove finalizer")
	updated := storeClient.updated.(*privatev1.Cluster)
	require.NotContains(t, updated.Finalizers, constants.FinalizerCluster)
}

// TestReconcile_Deletion_TransportError verifies that a transport.Delete error is
// propagated and the finalizer is NOT removed (controller will retry).
func TestReconcile_Deletion_TransportError(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := buildReadyCluster(clusterID, "4.15.0")
	cluster.Status.Conditions = append(cluster.Status.Conditions, metav1.Condition{
		Type: "ResourcesApplied", Status: metav1.ConditionTrue, Reason: "Applied",
	})
	now := metav1.Now()
	cluster.SetDeletionTimestamp(&now)

	tr := &errTransport{deleteErr: fmt.Errorf("firestore unavailable")}
	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	_, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.Error(t, err)
	require.Contains(t, err.Error(), "delete resources")
	require.False(t, storeClient.updateCalled, "finalizer should not be removed on error")
}

// TestReconcile_Deletion_NoFinalizer verifies that when the cluster has a DeletionTimestamp
// but no finalizer, the reconciler returns immediately (nothing to do).
func TestReconcile_Deletion_NoFinalizer(t *testing.T) {
	clusterID := "cluster-abc"
	cluster := buildReadyCluster(clusterID, "4.15.0")
	cluster.SetFinalizers(nil)
	now := metav1.Now()
	cluster.SetDeletionTimestamp(&now)

	tr := mock.New()
	r, storeClient := buildReconciler(t, cluster, nil, tr, nil)

	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)
	require.Empty(t, tr.DeleteCalls)
	require.False(t, storeClient.updateCalled)
}

// TestReconcile_Deletion_RemoveFinalizerError verifies that an error removing the
// finalizer after successful deletion/cleanup is propagated.
func TestReconcile_Deletion_RemoveFinalizerError(t *testing.T) {
	clusterID := "cluster-abc"
	mcName := "mc-cluster-1"
	cluster := buildReadyCluster(clusterID, "4.15.0")
	cluster.Status.Conditions = append(cluster.Status.Conditions, metav1.Condition{
		Type: "ResourcesApplied", Status: metav1.ConditionTrue, Reason: "Applied",
	})
	now := metav1.Now()
	cluster.SetDeletionTimestamp(&now)

	tr := mock.New()
	r, storeClient := buildReconciler(t, cluster, nil, tr, nil, func(m *mockStoreClient) {
		m.updateErr = fmt.Errorf("etcd write error")
	})

	// Simulate ApplyDesires exist (deletion not started).
	key := mcName + "/" + clusterID
	tr.DeleteStatusOverrides[key] = &transport.DeleteStatus{
		AllSuccessful:     false,
		TotalCount:        0,
		PendingCount:      0,
		ApplyDesiresCount: 3,
	}

	// First reconcile: initiate deletion, requeues.
	result, err := r.Reconcile(context.Background(), clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter)
	require.Len(t, tr.DeleteCalls, 1)

	// Simulate completion.
	tr.DeleteStatusOverrides[key] = &transport.DeleteStatus{AllSuccessful: true, TotalCount: 3, PendingCount: 0, ApplyDesiresCount: 0}

	// Second reconcile: cleanup succeeds, but finalizer removal fails.
	_, err = r.Reconcile(context.Background(), clusterReq(clusterID))
	require.Error(t, err)
	require.Contains(t, err.Error(), "remove finalizer")
	require.Len(t, tr.CleanupDeleteDesiresCalls, 1, "cleanup should have been called before finalizer removal failed")
	require.True(t, storeClient.updateCalled)
}
