//go:build integration

package firestore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	fstransport "github.com/openshift-online/gecko/controllers/client/transport/firestore"
	"github.com/openshift-online/gecko/controllers/util/logger"
	"github.com/openshift-online/kube-applier-gcp/pkg/api/kubeapplier"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// testMCName is the GCP project ID that identifies the MC.
	testMCName    = "test-project"
	testClusterID = "cluster-abc"
)

func emulatorOpts(t *testing.T) []option.ClientOption {
	t.Helper()
	host := os.Getenv("FIRESTORE_EMULATOR_HOST")
	if host == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set — skipping integration test")
	}
	return []option.ClientOption{
		option.WithEndpoint(host),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	}
}

func newTestClient(t *testing.T) *fstransport.Client {
	t.Helper()
	log, err := logger.NewLogger(logger.DefaultConfig())
	require.NoError(t, err)
	opts := emulatorOpts(t)
	return fstransport.New(log, opts...)
}

func hcManifest(t *testing.T, clusterID, clusterName string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"apiVersion": "hypershift.openshift.io/v1beta1",
		"kind":       "HostedCluster",
		"metadata":   map[string]any{"name": clusterName, "namespace": fmt.Sprintf("clusters-%s", clusterID)},
	})
	require.NoError(t, err)
	return raw
}

func npManifest(t *testing.T, clusterID, npName string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"apiVersion": "hypershift.openshift.io/v1beta1",
		"kind":       "NodePool",
		"metadata":   map[string]any{"name": npName, "namespace": fmt.Sprintf("clusters-%s", clusterID)},
	})
	require.NoError(t, err)
	return raw
}

// clearCollection deletes all documents in a collection (used between tests).
func clearCollection(ctx context.Context, t *testing.T, client *firestore.Client, coll string) {
	t.Helper()
	snaps, err := client.Collection(coll).Documents(ctx).GetAll()
	require.NoError(t, err)
	for _, snap := range snaps {
		_, err := snap.Ref.Delete(ctx)
		require.NoError(t, err)
	}
}

// applyDesireSpec builds a minimal ApplyDesireSpec for the given clusterID and resource name.
func applyDesireSpec(clusterID, name string) kubeapplier.ApplyDesireSpec {
	return kubeapplier.ApplyDesireSpec{
		ManagementCluster: testMCName,
		ClusterID:         clusterID,
		TargetItem: kubeapplier.ResourceReference{
			Group:     "hypershift.openshift.io",
			Version:   "v1beta1",
			Resource:  "hostedclusters",
			Namespace: fmt.Sprintf("clusters-%s", clusterID),
			Name:      name,
		},
	}
}

// specsApplyDesire returns an ApplyDesire suitable for writing to the specs DB.
func specsApplyDesire(clusterID, name string) kubeapplier.ApplyDesire {
	return kubeapplier.ApplyDesire{Spec: applyDesireSpec(clusterID, name)}
}

// statusApplyDesire returns an ApplyDesire as kube-applier-gcp writes it to the
// status DB: spec fields are empty, status carries the applied conditions.
func statusApplyDesire(conditions []metav1.Condition) kubeapplier.ApplyDesire {
	return kubeapplier.ApplyDesire{
		Status: kubeapplier.ApplyDesireStatus{Conditions: conditions},
	}
}

func TestIntegration_Apply_WritesApplyAndReadDesires(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := newTestClient(t)
	opts := emulatorOpts(t)

	specsClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "specs", opts...)
	require.NoError(t, err)
	defer specsClient.Close()
	clearCollection(ctx, t, specsClient, "applydesires")
	clearCollection(ctx, t, specsClient, "readdesires")
	defer clearCollection(ctx, t, specsClient, "applydesires")
	defer clearCollection(ctx, t, specsClient, "readdesires")

	manifests := [][]byte{
		hcManifest(t, testClusterID, "my-hc"),
	}

	_, err = c.Apply(ctx, testMCName, testClusterID, manifests)
	require.NoError(t, err)

	snaps, err := specsClient.Collection("applydesires").
		Where("spec.clusterID", "==", testClusterID).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	assert.Len(t, snaps, 1, "expected 1 ApplyDesire for the HostedCluster manifest")

	data := snaps[0].Data()
	assert.NotContains(t, data, "manifestWorkName")
	assert.NotNil(t, data["spec_kubeContent"])

	readSnaps, err := specsClient.Collection("readdesires").
		Where("spec.clusterID", "==", testClusterID).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	assert.Len(t, readSnaps, 1, "expected 1 ReadDesire for the HostedCluster manifest")
}

func TestIntegration_GetStatus_AllSuccessful(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := newTestClient(t)
	opts := emulatorOpts(t)

	specsClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "specs", opts...)
	require.NoError(t, err)
	defer specsClient.Close()

	statusClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "status", opts...)
	require.NoError(t, err)
	defer statusClient.Close()

	clearCollection(ctx, t, specsClient, "applydesires")
	clearCollection(ctx, t, statusClient, "applydesires")
	clearCollection(ctx, t, statusClient, "readdesires")
	defer clearCollection(ctx, t, specsClient, "applydesires")
	defer clearCollection(ctx, t, statusClient, "applydesires")
	defer clearCollection(ctx, t, statusClient, "readdesires")

	const docID = "doc-1"

	_, err = specsClient.Collection("applydesires").Doc(docID).Set(ctx, specsApplyDesire(testClusterID, "my-hc"))
	require.NoError(t, err)

	_, err = statusClient.Collection("applydesires").Doc(docID).Set(ctx, statusApplyDesire([]metav1.Condition{
		{Type: "Successful", Status: metav1.ConditionTrue, Reason: "NoErrors"},
	}))
	require.NoError(t, err)

	status, err := c.GetStatus(ctx, testMCName, testClusterID)
	require.NoError(t, err)
	require.Len(t, status.Conditions, 1)
	assert.Equal(t, "Applied", status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, status.Conditions[0].Status)
}

// TestIntegration_GetStatus_MissingStatusDocReportsPending verifies that when
// some resources have no status document yet, GetStatus reports Applied=False
// rather than prematurely reporting Applied=True based on the subset that do.
func TestIntegration_GetStatus_MissingStatusDocReportsPending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := newTestClient(t)
	opts := emulatorOpts(t)

	specsClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "specs", opts...)
	require.NoError(t, err)
	defer specsClient.Close()

	statusClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "status", opts...)
	require.NoError(t, err)
	defer statusClient.Close()

	clearCollection(ctx, t, specsClient, "applydesires")
	clearCollection(ctx, t, statusClient, "applydesires")
	defer clearCollection(ctx, t, specsClient, "applydesires")
	defer clearCollection(ctx, t, statusClient, "applydesires")

	// Two resources in specs DB.
	_, err = specsClient.Collection("applydesires").Doc("doc-1").Set(ctx, specsApplyDesire(testClusterID, "hc-1"))
	require.NoError(t, err)
	_, err = specsClient.Collection("applydesires").Doc("doc-2").Set(ctx, specsApplyDesire(testClusterID, "hc-2"))
	require.NoError(t, err)

	// Only doc-1 has a status doc with Successful=True; doc-2 is missing.
	_, err = statusClient.Collection("applydesires").Doc("doc-1").Set(ctx, statusApplyDesire([]metav1.Condition{
		{Type: "Successful", Status: metav1.ConditionTrue, Reason: "NoErrors"},
	}))
	require.NoError(t, err)

	status, err := c.GetStatus(ctx, testMCName, testClusterID)
	require.NoError(t, err)
	require.Len(t, status.Conditions, 1)
	assert.Equal(t, "Applied", status.Conditions[0].Type)
	// Must be False/Unknown — not True — because doc-2 has no status yet.
	assert.NotEqual(t, metav1.ConditionTrue, status.Conditions[0].Status)
}

func TestIntegration_GetStatus_ExtractsHCKubeContent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := newTestClient(t)
	opts := emulatorOpts(t)

	specsClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "specs", opts...)
	require.NoError(t, err)
	defer specsClient.Close()

	statusClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "status", opts...)
	require.NoError(t, err)
	defer statusClient.Close()

	clearCollection(ctx, t, specsClient, "readdesires")
	clearCollection(ctx, t, statusClient, "readdesires")
	defer clearCollection(ctx, t, specsClient, "readdesires")
	defer clearCollection(ctx, t, statusClient, "readdesires")

	const docID = "rd-1"

	specsReadDesire := kubeapplier.ReadDesire{
		Spec: kubeapplier.ReadDesireSpec{
			ManagementCluster: testMCName,
			ClusterID:         testClusterID,
			TargetItem: kubeapplier.ResourceReference{
				Group:     "hypershift.openshift.io",
				Version:   "v1beta1",
				Resource:  "hostedclusters",
				Namespace: "clusters-abc",
				Name:      "my-hc",
			},
		},
	}
	_, err = specsClient.Collection("readdesires").Doc(docID).Set(ctx, specsReadDesire)
	require.NoError(t, err)

	// Status DB: spec fields empty (kube-applier-gcp doesn't copy them),
	// status_kubeContent carries the live object — stored at doc root, not in the struct.
	hcLiveObject := map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Available", "status": "True"},
			},
			"controlPlaneEndpoint": map[string]any{"host": "api.my-hc.example.com"},
			"version": map[string]any{
				"history": []any{map[string]any{"version": "4.14.1", "state": "Completed"}},
			},
		},
	}
	_, err = statusClient.Collection("readdesires").Doc(docID).Set(ctx, map[string]any{
		"spec":               kubeapplier.ReadDesireSpec{},
		"status":             kubeapplier.ReadDesireStatus{},
		"status_kubeContent": hcLiveObject,
	})
	require.NoError(t, err)

	status, err := c.GetStatus(ctx, testMCName, testClusterID)
	require.NoError(t, err)

	key := "hypershift.openshift.io/v1beta1/hostedclusters/clusters-abc/my-hc"
	require.Contains(t, status.ResourceStatuses, key)
	assert.Equal(t, "True", status.ResourceStatuses[key]["availableCondition"])
	assert.Equal(t, "api.my-hc.example.com", status.ResourceStatuses[key]["controlPlaneEndpoint"])
	assert.Equal(t, "4.14.1", status.ResourceStatuses[key]["version"])
}

func TestIntegration_Delete_WritesDeleteDesireAndRemovesApplyRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := newTestClient(t)
	opts := emulatorOpts(t)

	specsClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "specs", opts...)
	require.NoError(t, err)
	defer specsClient.Close()

	clearCollection(ctx, t, specsClient, "applydesires")
	clearCollection(ctx, t, specsClient, "readdesires")
	clearCollection(ctx, t, specsClient, "deletedesires")
	defer clearCollection(ctx, t, specsClient, "applydesires")
	defer clearCollection(ctx, t, specsClient, "readdesires")
	defer clearCollection(ctx, t, specsClient, "deletedesires")

	manifests := [][]byte{
		npManifest(t, testClusterID, "my-np"),
	}

	_, err = c.Apply(ctx, testMCName, testClusterID, manifests)
	require.NoError(t, err)

	err = c.Delete(ctx, testMCName, testClusterID)
	require.NoError(t, err)

	applySnaps, err := specsClient.Collection("applydesires").
		Where("spec.clusterID", "==", testClusterID).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	assert.Empty(t, applySnaps, "ApplyDesires should be deleted")

	readSnaps, err := specsClient.Collection("readdesires").
		Where("spec.clusterID", "==", testClusterID).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	assert.Empty(t, readSnaps, "ReadDesires should be deleted")

	deleteSnaps, err := specsClient.Collection("deletedesires").Documents(ctx).GetAll()
	require.NoError(t, err)
	assert.Len(t, deleteSnaps, 1, "DeleteDesire should have been written")

	var dd kubeapplier.DeleteDesire
	require.NoError(t, deleteSnaps[0].DataTo(&dd))
	assert.Equal(t, testClusterID, dd.Spec.ClusterID)
}

// TestIntegration_Delete_ChunksLargeBatches verifies that Delete correctly
// processes more resources than the Firestore 500-write-per-transaction limit
// by chunking them into multiple transactions.
func TestIntegration_Delete_ChunksLargeBatches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := newTestClient(t)
	opts := emulatorOpts(t)

	specsClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "specs", opts...)
	require.NoError(t, err)
	defer specsClient.Close()

	clearCollection(ctx, t, specsClient, "applydesires")
	clearCollection(ctx, t, specsClient, "readdesires")
	clearCollection(ctx, t, specsClient, "deletedesires")
	defer clearCollection(ctx, t, specsClient, "applydesires")
	defer clearCollection(ctx, t, specsClient, "readdesires")
	defer clearCollection(ctx, t, specsClient, "deletedesires")

	// Seed 200 ApplyDesire + ReadDesire docs — enough to require 2 chunks
	// (maxDeleteBatchSize = 166, so 200 requires 2 transactions).
	const resourceCount = 200
	const chunkedClusterID = "cluster-chunked"

	batch := specsClient.BulkWriter(ctx)
	for i := range resourceCount {
		name := fmt.Sprintf("resource-%d", i)
		docID := fmt.Sprintf("chunked-%d", i)

		ad := specsApplyDesire(chunkedClusterID, name)
		applyRef := specsClient.Collection("applydesires").Doc(docID)
		_, err := batch.Set(applyRef, ad)
		require.NoError(t, err)

		rd := kubeapplier.ReadDesire{Spec: kubeapplier.ReadDesireSpec{
			ManagementCluster: testMCName,
			ClusterID:         chunkedClusterID,
			TargetItem:        ad.Spec.TargetItem,
		}}
		readRef := specsClient.Collection("readdesires").Doc(docID)
		_, err = batch.Set(readRef, rd)
		require.NoError(t, err)
	}
	batch.Flush()

	err = c.Delete(ctx, testMCName, chunkedClusterID)
	require.NoError(t, err)

	applySnaps, err := specsClient.Collection("applydesires").
		Where("spec.clusterID", "==", chunkedClusterID).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	assert.Empty(t, applySnaps, "all ApplyDesires should be deleted")

	readSnaps, err := specsClient.Collection("readdesires").
		Where("spec.clusterID", "==", chunkedClusterID).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	assert.Empty(t, readSnaps, "all ReadDesires should be deleted")

	deleteSnaps, err := specsClient.Collection("deletedesires").Documents(ctx).GetAll()
	require.NoError(t, err)
	assert.Len(t, deleteSnaps, resourceCount, "one DeleteDesire per resource")
}

// TestIntegration_Apply_StaleWhenStatusNotProcessed verifies that Apply returns
// Stale=true when the status DB has not yet been updated by kube-applier-gcp
// (i.e. ObservedDesireUpdateTime does not match the spec write timestamp).
func TestIntegration_Apply_StaleWhenStatusNotProcessed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := newTestClient(t)
	opts := emulatorOpts(t)

	specsClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "specs", opts...)
	require.NoError(t, err)
	defer specsClient.Close()

	statusClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "status", opts...)
	require.NoError(t, err)
	defer statusClient.Close()

	clearCollection(ctx, t, specsClient, "applydesires")
	clearCollection(ctx, t, specsClient, "readdesires")
	clearCollection(ctx, t, statusClient, "applydesires")
	clearCollection(ctx, t, statusClient, "readdesires")
	defer clearCollection(ctx, t, specsClient, "applydesires")
	defer clearCollection(ctx, t, specsClient, "readdesires")
	defer clearCollection(ctx, t, statusClient, "applydesires")
	defer clearCollection(ctx, t, statusClient, "readdesires")

	manifests := [][]byte{npManifest(t, testClusterID, "stale-np")}

	// First Apply — no status docs exist yet → stale.
	status, err := c.Apply(ctx, testMCName, testClusterID, manifests)
	require.NoError(t, err)
	assert.True(t, status.Stale, "should be stale when no status docs exist")

	// Simulate kube-applier-gcp processing: write status docs with a
	// mismatched ObservedDesireUpdateTime (an old timestamp).
	applySnaps, err := specsClient.Collection("applydesires").
		Where("spec.clusterID", "==", testClusterID).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	require.Len(t, applySnaps, 1)

	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = statusClient.Collection("applydesires").Doc(applySnaps[0].Ref.ID).Set(ctx, map[string]any{
		"spec": kubeapplier.ApplyDesireSpec{},
		"status": kubeapplier.ApplyDesireStatus{
			Conditions:               []metav1.Condition{{Type: "Successful", Status: metav1.ConditionTrue, Reason: "NoErrors"}},
			ObservedDesireUpdateTime: oldTime,
		},
	})
	require.NoError(t, err)

	readSnaps, err := specsClient.Collection("readdesires").
		Where("spec.clusterID", "==", testClusterID).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	require.Len(t, readSnaps, 1)

	_, err = statusClient.Collection("readdesires").Doc(readSnaps[0].Ref.ID).Set(ctx, map[string]any{
		"spec": kubeapplier.ReadDesireSpec{},
		"status": kubeapplier.ReadDesireStatus{
			ObservedDesireUpdateTime: oldTime,
		},
	})
	require.NoError(t, err)

	// Second Apply — status docs exist but with old timestamps → stale.
	status, err = c.Apply(ctx, testMCName, testClusterID, manifests)
	require.NoError(t, err)
	assert.True(t, status.Stale, "should be stale when ObservedDesireUpdateTime is old")

	// Now simulate kube-applier-gcp catching up: update status docs with the
	// current spec write timestamps. We need to read the spec UpdateTime.
	applySnaps, err = specsClient.Collection("applydesires").
		Where("spec.clusterID", "==", testClusterID).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	require.Len(t, applySnaps, 1)

	_, err = statusClient.Collection("applydesires").Doc(applySnaps[0].Ref.ID).Set(ctx, map[string]any{
		"spec": kubeapplier.ApplyDesireSpec{},
		"status": kubeapplier.ApplyDesireStatus{
			Conditions:               []metav1.Condition{{Type: "Successful", Status: metav1.ConditionTrue, Reason: "NoErrors"}},
			ObservedDesireUpdateTime: applySnaps[0].UpdateTime,
		},
	})
	require.NoError(t, err)

	readSnaps, err = specsClient.Collection("readdesires").
		Where("spec.clusterID", "==", testClusterID).
		Documents(ctx).GetAll()
	require.NoError(t, err)
	require.Len(t, readSnaps, 1)

	_, err = statusClient.Collection("readdesires").Doc(readSnaps[0].Ref.ID).Set(ctx, map[string]any{
		"spec": kubeapplier.ReadDesireSpec{},
		"status": kubeapplier.ReadDesireStatus{
			ObservedDesireUpdateTime: readSnaps[0].UpdateTime,
		},
	})
	require.NoError(t, err)

	// Third Apply — same manifests, status timestamps now match → not stale.
	status, err = c.Apply(ctx, testMCName, testClusterID, manifests)
	require.NoError(t, err)
	assert.False(t, status.Stale, "should not be stale when ObservedDesireUpdateTime matches")
}

// TestIntegration_GetStatus_NeverStale verifies that GetStatus (without Apply)
// never reports stale, since there are no write timestamps to compare against.
func TestIntegration_GetStatus_NeverStale(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := newTestClient(t)
	opts := emulatorOpts(t)

	specsClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "specs", opts...)
	require.NoError(t, err)
	defer specsClient.Close()

	statusClient, err := firestore.NewClientWithDatabase(ctx, testMCName, "status", opts...)
	require.NoError(t, err)
	defer statusClient.Close()

	clearCollection(ctx, t, specsClient, "applydesires")
	clearCollection(ctx, t, statusClient, "applydesires")
	defer clearCollection(ctx, t, specsClient, "applydesires")
	defer clearCollection(ctx, t, statusClient, "applydesires")

	const docID = "doc-getstatus"
	_, err = specsClient.Collection("applydesires").Doc(docID).Set(ctx, specsApplyDesire(testClusterID, "my-hc"))
	require.NoError(t, err)

	_, err = statusClient.Collection("applydesires").Doc(docID).Set(ctx, statusApplyDesire([]metav1.Condition{
		{Type: "Successful", Status: metav1.ConditionTrue, Reason: "NoErrors"},
	}))
	require.NoError(t, err)

	status, err := c.GetStatus(ctx, testMCName, testClusterID)
	require.NoError(t, err)
	assert.False(t, status.Stale, "GetStatus without Apply should never report stale")
}
