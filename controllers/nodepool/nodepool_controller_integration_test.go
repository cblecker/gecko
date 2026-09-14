package nodepool

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/openshift-online/kube-applier-gcp/pkg/api/kubeapplier"
	"github.com/openshift-online/kube-applier-gcp/pkg/desireid"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	fstransport "github.com/openshift-online/gecko/controllers/client/transport/firestore"
)

func nodePoolEmulatorOpts(t *testing.T) []option.ClientOption {
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

func clearNodePoolCollection(ctx context.Context, t *testing.T, client *firestore.Client, collection string) {
	t.Helper()
	snapshots, err := client.Collection(collection).Documents(ctx).GetAll()
	require.NoError(t, err)
	for _, snapshot := range snapshots {
		_, err := snapshot.Ref.Delete(ctx)
		require.NoError(t, err)
	}
}

func cleanupNodePoolCollections(t *testing.T, clients ...*firestore.Client) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, client := range clients {
			clearNodePoolCollection(ctx, t, client, "applydesires")
			clearNodePoolCollection(ctx, t, client, "readdesires")
		}
	})
}

func TestIntegration_NodePool_ApplyAndStatusReadback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := nodePoolEmulatorOpts(t)
	project := fmt.Sprintf("gecko-np-%d", time.Now().UnixNano())
	specsClient, err := firestore.NewClientWithDatabase(ctx, project, "specs", opts...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, specsClient.Close()) })
	statusClient, err := firestore.NewClientWithDatabase(ctx, project, "status", opts...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, statusClient.Close()) })

	cleanupNodePoolCollections(t, specsClient, statusClient)

	transportClient := fstransport.New(newTestLogger(t), opts...)
	defer transportClient.Close()

	np := testNodePool("4.16.0")
	np.SetGeneration(3)
	cluster := testCluster(true, true)
	clusterUID := "550e8400-e29b-41d4-a716-446655440002"
	cluster.SetUID(types.UID(clusterUID))
	cluster.Spec.SafeName = privatev1.DefaultSafeName(cluster.Name, cluster.UID)
	cluster.SetNamespace(np.Namespace)
	cluster.Status.PlacementResult.ManagementClusterName = project
	groupKey := mustNodePoolGroupKey(np.Namespace, cluster.Name, np.Name)
	r, storeClient := buildReconciler(t, np, cluster, transportClient, nil, nil, nil)

	result, err := r.Reconcile(ctx, npReq(np.Spec.ClusterID, np.Name))
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, result.RequeueAfter)
	require.False(t, storeClient.statusWriter.called)

	expectedTarget := kubeapplier.ResourceReference{
		Group:     "hypershift.openshift.io",
		Version:   "v1beta1",
		Resource:  "nodepools",
		Namespace: "clusters-" + clusterUID,
		Name:      "np-test",
	}
	expectedID := desireid.NewDocumentID(
		groupKey,
		expectedTarget.Group,
		expectedTarget.Version,
		expectedTarget.Resource,
		expectedTarget.Namespace,
		expectedTarget.Name,
	)

	applySnapshots, err := specsClient.Collection("applydesires").
		Where("spec.groupKey", "==", groupKey).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	require.Len(t, applySnapshots, 1)
	require.Equal(t, expectedID, applySnapshots[0].Ref.ID)

	var applyDesire kubeapplier.ApplyDesire
	require.NoError(t, applySnapshots[0].DataTo(&applyDesire))
	require.Equal(t, project, applyDesire.Spec.ManagementCluster)
	require.Equal(t, groupKey, applyDesire.Spec.GroupKey)
	require.Equal(t, expectedTarget, applyDesire.Spec.TargetItem)

	content, ok := applySnapshots[0].Data()["spec_kubeContent"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "hypershift.openshift.io/v1beta1", content["apiVersion"])
	require.Equal(t, "NodePool", content["kind"])
	metadata, ok := content["metadata"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, np.Name, metadata["name"])
	require.Equal(t, expectedTarget.Namespace, metadata["namespace"])
	labels, ok := metadata["labels"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, clusterUID, labels["gcp.managed.openshift.io/cluster-id"])
	require.Equal(t, np.Name, labels["gcp.managed.openshift.io/nodepool-id"])
	annotations, ok := metadata["annotations"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "3", annotations["gcp.managed.openshift.io/generation"])
	manifestSpec, ok := content["spec"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, cluster.Spec.SafeName, manifestSpec["clusterName"])
	require.EqualValues(t, 1, manifestSpec["replicas"])
	release, ok := manifestSpec["release"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, np.Status.VersionResolution.ReleaseImage, release["image"])
	platform, ok := manifestSpec["platform"].(map[string]any)
	require.True(t, ok)
	gcp, ok := platform["gcp"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, np.Spec.Platform.GCP.MachineType, gcp["machineType"])
	require.Equal(t, np.Spec.Platform.GCP.Zone, gcp["zone"])
	require.Equal(t, cluster.Spec.Platform.GCP.Subnet, gcp["subnet"])

	readSnapshots, err := specsClient.Collection("readdesires").
		Where("spec.groupKey", "==", groupKey).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	require.Len(t, readSnapshots, 1)
	require.Equal(t, expectedID, readSnapshots[0].Ref.ID)
	var readDesire kubeapplier.ReadDesire
	require.NoError(t, readSnapshots[0].DataTo(&readDesire))
	require.Equal(t, expectedTarget, readDesire.Spec.TargetItem)

	_, err = statusClient.Collection("applydesires").Doc(expectedID).Set(ctx, map[string]any{
		"spec": applyDesire.Spec,
		"status": kubeapplier.ApplyDesireStatus{
			Conditions: []metav1.Condition{{
				Type:   kubeapplier.ConditionTypeSuccessful,
				Status: metav1.ConditionTrue,
				Reason: "NoErrors",
			}},
			ObservedDesireUpdateTime: applySnapshots[0].UpdateTime,
		},
	})
	require.NoError(t, err)
	_, err = statusClient.Collection("readdesires").Doc(expectedID).Set(ctx, map[string]any{
		"spec": readDesire.Spec,
		"status": kubeapplier.ReadDesireStatus{
			ObservedDesireUpdateTime: readSnapshots[0].UpdateTime,
		},
		"status_kubeContent": map[string]any{
			"status": map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "True"},
					map[string]any{"type": "AllNodesHealthy", "status": "True"},
					map[string]any{"type": "AllMachinesReady", "status": "True"},
					map[string]any{"type": "UpdatingConfig", "status": "False"},
					map[string]any{"type": "UpdatingVersion", "status": "False"},
				},
			},
		},
	})
	require.NoError(t, err)

	result, err = r.Reconcile(ctx, npReq(np.Spec.ClusterID, np.Name))
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, result.RequeueAfter)
	require.True(t, storeClient.statusWriter.called)

	captured, ok := storeClient.statusWriter.captured.(*privatev1.NodePool)
	require.True(t, ok)
	resourcesApplied := meta.FindStatusCondition(captured.Status.Conditions, "NodePoolResourcesApplied")
	require.NotNil(t, resourcesApplied)
	require.Equal(t, metav1.ConditionTrue, resourcesApplied.Status)
	require.Equal(t, "AllResourcesApplied", resourcesApplied.Reason)
	available := meta.FindStatusCondition(captured.Status.Conditions, "NodePoolAvailable")
	require.NotNil(t, available)
	require.Equal(t, metav1.ConditionTrue, available.Status)
	require.Equal(t, "NodePoolAvailable", available.Reason)
	healthy := meta.FindStatusCondition(captured.Status.Conditions, "NodePoolHealthy")
	require.NotNil(t, healthy)
	require.Equal(t, metav1.ConditionTrue, healthy.Status)
	require.Equal(t, "NodePoolHealthy", healthy.Reason)
	progressing := meta.FindStatusCondition(captured.Status.Conditions, "NodePoolProgressing")
	require.NotNil(t, progressing)
	require.Equal(t, metav1.ConditionFalse, progressing.Status)
	require.Equal(t, "AsExpected", progressing.Reason)
}
