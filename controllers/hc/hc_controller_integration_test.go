package hc_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	fstransport "github.com/openshift-online/gecko/controllers/client/transport/firestore"
	"github.com/openshift-online/gecko/controllers/hc"
	"github.com/openshift-online/gecko/controllers/util/constants"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
	"github.com/openshift-online/kube-applier-gcp/pkg/api/kubeapplier"
	"github.com/openshift-online/kube-applier-gcp/pkg/desireid"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

type hcExpectedResource struct {
	group     string
	version   string
	resource  string
	namespace string
	name      string
	kind      string
}

func hcEmulatorOpts(t *testing.T) []option.ClientOption {
	t.Helper()
	host := os.Getenv("FIRESTORE_EMULATOR_HOST")
	if host == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set; skipping integration test")
	}
	return []option.ClientOption{
		option.WithEndpoint(host),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	}
}

func clearHCCollection(ctx context.Context, t *testing.T, client *firestore.Client, collection string) {
	t.Helper()
	snapshots, err := client.Collection(collection).Documents(ctx).GetAll()
	require.NoError(t, err)
	for _, snapshot := range snapshots {
		_, err := snapshot.Ref.Delete(ctx)
		require.NoError(t, err)
	}
}

func cleanupHCCollections(t *testing.T, clients ...*firestore.Client) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, client := range clients {
			clearHCCollection(ctx, t, client, "applydesires")
			clearHCCollection(ctx, t, client, "readdesires")
			clearHCCollection(ctx, t, client, "deletedesires")
		}
	})
}

func TestIntegration_HC_ApplyAndStatusReadback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := hcEmulatorOpts(t)
	project := fmt.Sprintf("gecko-hc-%d", time.Now().UnixNano())
	specsClient, err := firestore.NewClientWithDatabase(ctx, project, "specs", opts...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, specsClient.Close()) })
	statusClient, err := firestore.NewClientWithDatabase(ctx, project, "status", opts...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, statusClient.Close()) })

	cleanupHCCollections(t, specsClient, statusClient)

	transportClient := fstransport.New(testLogger(t), opts...)
	defer transportClient.Close()

	const clusterID = "cluster-integration"
	const clusterUID = "550e8400-e29b-41d4-a716-446655440001"
	cluster := buildReadyCluster(clusterID, "4.15.0")
	cluster.SetUID(types.UID(clusterUID))
	safeName := privatev1.DefaultSafeName(cluster.Name, cluster.UID)
	cluster.Spec.SafeName = safeName
	cluster.Status.PlacementResult.ManagementClusterName = project
	groupKey := mustClusterGroupKey(cluster.Namespace, cluster.Name)
	r, storeClient := buildReconciler(t, cluster, nil, transportClient, nil)

	result, err := r.Reconcile(ctx, clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter)
	require.True(t, storeClient.statusWriter.called)

	// Tag comparison is known immediately from the specs transaction, even
	// before kube-applier has provided any apply or availability feedback.
	initialStatus, ok := storeClient.statusWriter.captured.(*privatev1.Cluster)
	require.True(t, ok)
	tagsMismatch := meta.FindStatusCondition(initialStatus.Status.Conditions, "ResourceTagsMismatch")
	require.NotNil(t, tagsMismatch)
	require.Equal(t, metav1.ConditionFalse, tagsMismatch.Status)
	require.Equal(t, "TagsMatch", tagsMismatch.Reason)
	require.Equal(t, cluster.Generation, tagsMismatch.ObservedGeneration)
	for _, conditionType := range []string{"ResourcesApplied", "HostedClusterAvailable", "ApiCertificateReady"} {
		require.Nil(t, meta.FindStatusCondition(initialStatus.Status.Conditions, conditionType),
			"must not derive %s from stale feedback", conditionType)
	}
	require.Nil(t, initialStatus.Status.HostedClusterResult)

	clusterNamespace := fmt.Sprintf("clusters-%s", clusterUID)
	expected := []hcExpectedResource{
		{version: "v1", resource: "namespaces", name: clusterNamespace, kind: "Namespace"},
		{group: "external-secrets.io", version: "v1", resource: "externalsecrets", namespace: clusterNamespace, name: "pull-secret", kind: "ExternalSecret"},
		{group: "cert-manager.io", version: "v1", resource: "certificates", namespace: clusterNamespace, name: "external-api-cert", kind: "Certificate"},
		{group: "hypershift.openshift.io", version: "v1beta1", resource: "hostedclusters", namespace: clusterNamespace, name: safeName, kind: "HostedCluster"},
		{group: "batch", version: "v1", resource: "jobs", namespace: fmt.Sprintf("clusters-%s-%s", clusterUID, safeName), name: "rbac-setup-gen-2", kind: "Job"},
	}
	for _, resource := range expected {
		require.LessOrEqual(t, len(resource.namespace), 63)
	}
	expectedByID := make(map[string]hcExpectedResource, len(expected))
	for _, resource := range expected {
		id := desireid.NewDocumentID(groupKey, resource.group, resource.version, resource.resource, resource.namespace, resource.name)
		expectedByID[id] = resource
	}

	applySnapshots, err := specsClient.Collection("applydesires").
		Where("spec.groupKey", "==", groupKey).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	require.Len(t, applySnapshots, len(expected))
	for _, snapshot := range applySnapshots {
		resource, found := expectedByID[snapshot.Ref.ID]
		require.True(t, found, "unexpected ApplyDesire document ID %q", snapshot.Ref.ID)

		var desire kubeapplier.ApplyDesire
		require.NoError(t, snapshot.DataTo(&desire))
		require.Equal(t, project, desire.Spec.ManagementCluster)
		require.Equal(t, groupKey, desire.Spec.GroupKey)
		require.Equal(t, resource.group, desire.Spec.TargetItem.Group)
		require.Equal(t, resource.version, desire.Spec.TargetItem.Version)
		require.Equal(t, resource.resource, desire.Spec.TargetItem.Resource)
		require.Equal(t, resource.namespace, desire.Spec.TargetItem.Namespace)
		require.Equal(t, resource.name, desire.Spec.TargetItem.Name)

		content, ok := snapshot.Data()["spec_kubeContent"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, resource.kind, content["kind"])
		metadata, ok := content["metadata"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, resource.name, metadata["name"])
		if resource.namespace != "" {
			require.Equal(t, resource.namespace, metadata["namespace"])
		}
	}

	readSnapshots, err := specsClient.Collection("readdesires").
		Where("spec.groupKey", "==", groupKey).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	require.Len(t, readSnapshots, len(expected))
	for _, snapshot := range readSnapshots {
		resource, found := expectedByID[snapshot.Ref.ID]
		require.True(t, found, "unexpected ReadDesire document ID %q", snapshot.Ref.ID)
		var desire kubeapplier.ReadDesire
		require.NoError(t, snapshot.DataTo(&desire))
		require.Equal(t, resource.resource, desire.Spec.TargetItem.Resource)
		require.Equal(t, resource.namespace, desire.Spec.TargetItem.Namespace)
		require.Equal(t, resource.name, desire.Spec.TargetItem.Name)
	}

	for _, snapshot := range applySnapshots {
		var desire kubeapplier.ApplyDesire
		require.NoError(t, snapshot.DataTo(&desire))
		_, err := statusClient.Collection("applydesires").Doc(snapshot.Ref.ID).Set(ctx, map[string]any{
			"spec": desire.Spec,
			"status": kubeapplier.ApplyDesireStatus{
				Conditions: []metav1.Condition{{
					Type:   kubeapplier.ConditionTypeSuccessful,
					Status: metav1.ConditionTrue,
					Reason: "NoErrors",
				}},
				ObservedDesireUpdateTime: snapshot.UpdateTime,
			},
		})
		require.NoError(t, err)
	}
	for _, snapshot := range readSnapshots {
		resource := expectedByID[snapshot.Ref.ID]
		var desire kubeapplier.ReadDesire
		require.NoError(t, snapshot.DataTo(&desire))
		data := map[string]any{
			"spec": desire.Spec,
			"status": kubeapplier.ReadDesireStatus{
				ObservedDesireUpdateTime: snapshot.UpdateTime,
			},
		}
		switch resource.resource {
		case "hostedclusters":
			data["status_kubeContent"] = map[string]any{
				"status": map[string]any{
					"conditions":           []any{map[string]any{"type": "Available", "status": "True"}},
					"controlPlaneEndpoint": map[string]any{"host": "api.cluster-integration.example.com"},
					"version": map[string]any{
						"history": []any{map[string]any{"version": "4.15.0", "state": "Completed"}},
					},
				},
			}
		case "certificates":
			data["status_kubeContent"] = map[string]any{
				"status": map[string]any{
					"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
				},
			}
		}
		_, err := statusClient.Collection("readdesires").Doc(snapshot.Ref.ID).Set(ctx, data)
		require.NoError(t, err)
	}

	// Model persistence of the first status write and ensure the next write
	// really comes from fresh kube-applier feedback, not the previous call.
	storeClient.cluster = initialStatus.DeepCopy()
	storeClient.statusWriter.called = false
	storeClient.statusWriter.captured = nil
	result, err = r.Reconcile(ctx, clusterReq(clusterID))
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, result.RequeueAfter)
	require.True(t, storeClient.statusWriter.called)

	captured, ok := storeClient.statusWriter.captured.(*privatev1.Cluster)
	require.True(t, ok)
	resourcesApplied := meta.FindStatusCondition(captured.Status.Conditions, "ResourcesApplied")
	require.NotNil(t, resourcesApplied)
	require.Equal(t, metav1.ConditionTrue, resourcesApplied.Status)
	require.Equal(t, "AllResourcesApplied", resourcesApplied.Reason)
	hostedClusterAvailable := meta.FindStatusCondition(captured.Status.Conditions, "HostedClusterAvailable")
	require.NotNil(t, hostedClusterAvailable)
	require.Equal(t, metav1.ConditionTrue, hostedClusterAvailable.Status)
	certificateReady := meta.FindStatusCondition(captured.Status.Conditions, "ApiCertificateReady")
	require.NotNil(t, certificateReady)
	require.Equal(t, metav1.ConditionTrue, certificateReady.Status)
	require.Equal(t, "CertificateReady", certificateReady.Reason)
	require.Equal(t, "api.cluster-integration.example.com", captured.Status.HostedClusterResult.APIEndpoint)
	require.Equal(t, "4.15.0", captured.Status.HostedClusterResult.Version)
}

func TestIntegration_HC_DeletionWaitsForNodePools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := hcEmulatorOpts(t)
	project := fmt.Sprintf("gecko-hc-delete-%d", time.Now().UnixNano())
	specsClient, err := firestore.NewClientWithDatabase(ctx, project, "specs", opts...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, specsClient.Close()) })
	statusClient, err := firestore.NewClientWithDatabase(ctx, project, "status", opts...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, statusClient.Close()) })
	cleanupHCCollections(t, specsClient, statusClient)

	transportClient := fstransport.New(testLogger(t), opts...)
	defer transportClient.Close()

	cluster := buildReadyCluster("cluster-integration", "4.15.0")
	cluster.Status.Conditions = append(cluster.Status.Conditions, metav1.Condition{
		Type: "ResourcesApplied", Status: metav1.ConditionTrue, Reason: "Applied",
	})
	now := metav1.Now()
	cluster.SetDeletionTimestamp(&now)
	cluster.Status.PlacementResult.ManagementClusterName = project
	groupKey := mustClusterGroupKey(cluster.Namespace, cluster.Name)

	_, err = transportClient.Apply(ctx, project, groupKey, [][]byte{[]byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"test","namespace":"test"}}`)})
	require.NoError(t, err)

	r, storeClient := buildReconciler(t, cluster, nil, transportClient, nil, func(m *mockStoreClient) {
		m.nodePools = []privatev1.NodePool{{
			ObjectMeta: metav1.ObjectMeta{Name: "workers", Namespace: cluster.Namespace},
			Spec:       privatev1.NodePoolSpec{ClusterID: cluster.Name},
		}}
	})

	result, err := r.Reconcile(ctx, clusterReq(cluster.Name))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter)
	require.Len(t, storeClient.deleted, 1)
	require.Equal(t, "workers", storeClient.deleted[0].GetName())
	require.False(t, storeClient.updateCalled, "Cluster finalizer must remain while the NodePool exists")

	deleteSnapshots, err := specsClient.Collection("deletedesires").
		Where("spec.groupKey", "==", groupKey).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	require.Len(t, deleteSnapshots, 1, "Cluster cleanup must start while NodePool deletion is pending")

	var deleteDesire kubeapplier.DeleteDesire
	require.NoError(t, deleteSnapshots[0].DataTo(&deleteDesire))
	deleteDesire.Status = kubeapplier.DeleteDesireStatus{
		Conditions: []metav1.Condition{{
			Type:   kubeapplier.ConditionTypeSuccessful,
			Status: metav1.ConditionTrue,
			Reason: "NoErrors",
		}},
		ObservedDesireUpdateTime: deleteSnapshots[0].UpdateTime,
	}
	_, err = statusClient.Collection("deletedesires").Doc(deleteSnapshots[0].Ref.ID).Set(ctx, deleteDesire)
	require.NoError(t, err)

	storeClient.nodePools = nil
	result, err = r.Reconcile(ctx, clusterReq(cluster.Name))
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)
	require.True(t, storeClient.updateCalled)
	updated := storeClient.updated.(*privatev1.Cluster)
	require.NotContains(t, updated.Finalizers, constants.FinalizerCluster)
}

// A new platform tag must not block unrelated updates to an existing 50-tag desire.
func TestIntegration_HC_PreservesTagsWhenDesiredUnionExceedsLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	opts := hcEmulatorOpts(t)
	project := fmt.Sprintf("gecko-hc-tag-limit-%d", time.Now().UnixNano())
	specs, err := firestore.NewClientWithDatabase(ctx, project, "specs", opts...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, specs.Close()) })
	cleanupHCCollections(t, specs)
	transportClient := fstransport.New(testLogger(t), opts...)
	defer transportClient.Close()
	cluster := buildReadyCluster("cluster-tag-limit", "4.15.0")
	cluster.Status.PlacementResult.ManagementClusterName = project
	for i := range 49 {
		cluster.Spec.Platform.GCP.ResourceTags = append(cluster.Spec.Platform.GCP.ResourceTags, privatev1.GCPResourceTag{Key: fmt.Sprintf("customer-%02d", i), Value: "v"})
	}
	_, store := buildReconciler(t, cluster, nil, transportClient, nil)
	platformTags := []privatev1.GCPResourceTag{{Key: "platform-a", Value: "v"}}
	r := hc.New(transportClient, testLogger(t), store, nil, platformTags, "", "")
	_, err = r.Reconcile(ctx, clusterReq(cluster.Name))
	require.NoError(t, err)
	docID := desireid.NewDocumentID(mustClusterGroupKey(cluster.Namespace, cluster.Name), "hypershift.openshift.io", "v1beta1", "hostedclusters", fmt.Sprintf("clusters-%s", cluster.UID), cluster.Spec.SafeName)
	doc := specs.Collection("applydesires").Doc(docID)
	initial, err := doc.Get(ctx)
	require.NoError(t, err)
	originalTags, found, err := unstructured.NestedSlice(initial.Data(), "spec_kubeContent", "spec", "platform", "gcp", "resourceTags")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, originalTags, 50)

	store.cluster = store.statusWriter.captured.(*privatev1.Cluster).DeepCopy()
	newRelease := "quay.io/example/release:updated"
	store.cluster.Status.VersionResolution.ReleaseImage = newRelease
	platformTags = append(platformTags, privatev1.GCPResourceTag{Key: "platform-b", Value: "v"})
	r = hc.New(transportClient, testLogger(t), store, nil, platformTags, "", "")
	for range 2 {
		store.statusWriter.called = false
		_, err = r.Reconcile(ctx, clusterReq(cluster.Name))
		require.NoError(t, err)
		updated, err := doc.Get(ctx)
		require.NoError(t, err)
		tags, found, err := unstructured.NestedSlice(updated.Data(), "spec_kubeContent", "spec", "platform", "gcp", "resourceTags")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, originalTags, tags)
		release, found, err := unstructured.NestedString(updated.Data(), "spec_kubeContent", "spec", "release", "image")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, newRelease, release, "unrelated updates must still be stored")
		if store.statusWriter.called {
			store.cluster = store.statusWriter.captured.(*privatev1.Cluster).DeepCopy()
		}
		mismatch := meta.FindStatusCondition(store.cluster.Status.Conditions, "ResourceTagsMismatch")
		require.NotNil(t, mismatch)
		require.Equal(t, metav1.ConditionTrue, mismatch.Status)
		require.Equal(t, "TagsPreserved", mismatch.Reason)
	}
}
