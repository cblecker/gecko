package manifest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuild_HappyPath(t *testing.T) {
	input := Input{
		NodePoolID:         "np-001",
		NodePoolName:       "my-nodepool",
		NodePoolGeneration: 3,
		ClusterID:          "550e8400-e29b-41d4-a716-446655440000",
		ClusterName:        "my-cluster-safe",
		Replicas:           2,
		MachineType:        "n2-standard-8",
		GCPRegion:          "us-central1",
		Zone:               "us-central1-b",
		GCPSubnet:          "my-subnet",
		DiskSizeGB:         200,
		DiskType:           "pd-standard",
		ReleaseImage:       "quay.io/openshift-release-dev/ocp-release:4.16.0-x86_64",
	}

	manifests, err := Build(input)
	require.NoError(t, err)
	require.NotNil(t, manifests)

	// Exactly 1 manifest
	require.Len(t, manifests, 1)

	// Unmarshal manifest and check fields
	var nodePool map[string]any
	require.NoError(t, json.Unmarshal(manifests[0], &nodePool))

	require.Equal(t, "hypershift.openshift.io/v1beta1", nodePool["apiVersion"])
	require.Equal(t, "NodePool", nodePool["kind"])

	meta := nodePool["metadata"].(map[string]any)
	require.Equal(t, "my-nodepool", meta["name"])
	require.Equal(t, "clusters-550e8400-e29b-41d4-a716-446655440000", meta["namespace"])

	metaLabels := meta["labels"].(map[string]any)
	require.Equal(t, "550e8400-e29b-41d4-a716-446655440000", metaLabels["gcp.managed.openshift.io/cluster-id"])
	require.Equal(t, "np-001", metaLabels["gcp.managed.openshift.io/nodepool-id"])
	require.Equal(t, "nodepool-controller", metaLabels["gcp.managed.openshift.io/managed-by"])

	metaAnnotations := meta["annotations"].(map[string]any)
	require.Equal(t, "3", metaAnnotations["gcp.managed.openshift.io/generation"])

	spec := nodePool["spec"].(map[string]any)
	require.Equal(t, "my-cluster-safe", spec["clusterName"])
	require.EqualValues(t, 2, spec["replicas"])
	require.Equal(t, "amd64", spec["arch"])

	release := spec["release"].(map[string]any)
	require.Equal(t, "quay.io/openshift-release-dev/ocp-release:4.16.0-x86_64", release["image"])

	platform := spec["platform"].(map[string]any)
	require.Equal(t, "GCP", platform["type"])
	gcp := platform["gcp"].(map[string]any)
	require.Equal(t, "n2-standard-8", gcp["machineType"])
	require.Equal(t, "us-central1-b", gcp["zone"])
	require.Equal(t, "my-subnet", gcp["subnet"])
	bootDisk := gcp["bootDisk"].(map[string]any)
	require.EqualValues(t, 200, bootDisk["diskSizeGB"])
	require.Equal(t, "pd-standard", bootDisk["diskType"])
}

func TestBuild_ZoneFallback(t *testing.T) {
	input := Input{
		NodePoolID:         "np-002",
		NodePoolName:       "np-fallback",
		NodePoolGeneration: 1,
		ClusterID:          "cluster-xyz",
		ClusterName:        "cl",
		GCPRegion:          "us-west1",
		Zone:               "", // should fall back
		ReleaseImage:       "quay.io/openshift-release-dev/ocp-release:4.16.0-x86_64",
	}

	manifests, err := Build(input)
	require.NoError(t, err)

	var nodePool map[string]any
	require.NoError(t, json.Unmarshal(manifests[0], &nodePool))

	spec := nodePool["spec"].(map[string]any)
	platform := spec["platform"].(map[string]any)
	gcp := platform["gcp"].(map[string]any)
	require.Equal(t, "us-west1-a", gcp["zone"])
}

func TestBuild_DiskDefaults(t *testing.T) {
	input := Input{
		NodePoolID:         "np-003",
		NodePoolName:       "np-defaults",
		NodePoolGeneration: 1,
		ClusterID:          "cluster-def",
		ClusterName:        "cl",
		GCPRegion:          "us-east1",
		ReleaseImage:       "quay.io/openshift-release-dev/ocp-release:4.16.0-x86_64",
		// DiskSizeGB, DiskType, MachineType intentionally zero/empty
	}

	manifests, err := Build(input)
	require.NoError(t, err)

	var nodePool map[string]any
	require.NoError(t, json.Unmarshal(manifests[0], &nodePool))

	spec := nodePool["spec"].(map[string]any)
	platform := spec["platform"].(map[string]any)
	gcp := platform["gcp"].(map[string]any)

	require.Equal(t, "n2-standard-4", gcp["machineType"])
	bootDisk := gcp["bootDisk"].(map[string]any)
	require.EqualValues(t, 100, bootDisk["diskSizeGB"])
	require.Equal(t, "pd-ssd", bootDisk["diskType"])
}

func TestBuild_ManifestCount(t *testing.T) {
	input := Input{
		NodePoolID:         "np-count",
		NodePoolName:       "np-count",
		NodePoolGeneration: 1,
		ClusterID:          "cid",
		ClusterName:        "cl",
		GCPRegion:          "us-central1",
		ReleaseImage:       "quay.io/openshift-release-dev/ocp-release:4.16.0-x86_64",
	}

	manifests, err := Build(input)
	require.NoError(t, err)
	require.Len(t, manifests, 1, "expected exactly 1 manifest (the NodePool CR)")
}
