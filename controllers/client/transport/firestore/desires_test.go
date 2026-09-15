package firestore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openshift-online/kube-applier-gcp/pkg/api/kubeapplier"
)

func TestParseManifest_HostedCluster(t *testing.T) {
	raw := []byte(`{"apiVersion":"hypershift.openshift.io/v1beta1","kind":"HostedCluster","metadata":{"name":"my-hc","namespace":"clusters-abc"}}`)
	ref, unknown, err := parseManifest(raw)
	require.NoError(t, err)
	assert.False(t, unknown)
	assert.Equal(t, "hypershift.openshift.io", ref.Group)
	assert.Equal(t, "v1beta1", ref.Version)
	assert.Equal(t, "hostedclusters", ref.Resource)
	assert.Equal(t, "clusters-abc", ref.Namespace)
	assert.Equal(t, "my-hc", ref.Name)
}

func TestParseManifest_Namespace(t *testing.T) {
	raw := []byte(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"clusters-abc"}}`)
	ref, unknown, err := parseManifest(raw)
	require.NoError(t, err)
	assert.False(t, unknown)
	assert.Equal(t, "", ref.Group)
	assert.Equal(t, "v1", ref.Version)
	assert.Equal(t, "namespaces", ref.Resource)
	assert.Equal(t, "", ref.Namespace)
	assert.Equal(t, "clusters-abc", ref.Name)
}

func TestParseManifest_NodePool(t *testing.T) {
	raw := []byte(`{"apiVersion":"hypershift.openshift.io/v1beta1","kind":"NodePool","metadata":{"name":"my-np","namespace":"clusters-abc"}}`)
	ref, unknown, err := parseManifest(raw)
	require.NoError(t, err)
	assert.False(t, unknown)
	assert.Equal(t, "hypershift.openshift.io", ref.Group)
	assert.Equal(t, "v1beta1", ref.Version)
	assert.Equal(t, "nodepools", ref.Resource)
	assert.Equal(t, "clusters-abc", ref.Namespace)
}

func TestParseManifest_Job(t *testing.T) {
	raw := []byte(`{"apiVersion":"batch/v1","kind":"Job","metadata":{"name":"rbac-setup-gen-1","namespace":"clusters-abc-mycluster"}}`)
	ref, unknown, err := parseManifest(raw)
	require.NoError(t, err)
	assert.False(t, unknown)
	assert.Equal(t, "batch", ref.Group)
	assert.Equal(t, "v1", ref.Version)
	assert.Equal(t, "jobs", ref.Resource)
}

func TestParseManifest_UnknownKind(t *testing.T) {
	raw := []byte(`{"apiVersion":"custom.io/v1","kind":"Widget","metadata":{"name":"my-widget","namespace":"default"}}`)
	ref, unknown, err := parseManifest(raw)
	require.NoError(t, err)
	assert.True(t, unknown, "unknown Kind should set unknownKind=true")
	assert.Equal(t, "widgets", ref.Resource, "fallback pluralization")
}

func TestParseManifest_MissingName(t *testing.T) {
	raw := []byte(`{"apiVersion":"v1","kind":"Namespace","metadata":{}}`)
	_, _, err := parseManifest(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing metadata.name")
}

func TestParseManifest_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		message string
	}{
		{name: "invalid JSON", raw: `{`, message: "unmarshal"},
		{name: "missing apiVersion", raw: `{"kind":"Namespace","metadata":{"name":"test"}}`, message: "missing apiVersion"},
		{name: "missing kind", raw: `{"apiVersion":"v1","metadata":{"name":"test"}}`, message: "missing kind"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := parseManifest([]byte(test.raw))
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestResourceKey(t *testing.T) {
	ref := kubeapplier.ResourceReference{
		Group:     "hypershift.openshift.io",
		Version:   "v1beta1",
		Resource:  "hostedclusters",
		Namespace: "clusters-abc",
		Name:      "my-hc",
	}
	assert.Equal(t, "hypershift.openshift.io/v1beta1/hostedclusters/clusters-abc/my-hc", resourceKey(ref))
}

func TestResourceKey_CoreGroup(t *testing.T) {
	ref := kubeapplier.ResourceReference{
		Group:    "",
		Version:  "v1",
		Resource: "namespaces",
		Name:     "clusters-abc",
	}
	assert.Equal(t, "/v1/namespaces//clusters-abc", resourceKey(ref))
}

func TestBuildApplyDesireDoc_DocumentIDDeterministic(t *testing.T) {
	ref := kubeapplier.ResourceReference{
		Group: "hypershift.openshift.io", Version: "v1beta1",
		Resource: "hostedclusters", Namespace: "clusters-abc", Name: "my-hc",
	}
	raw := []byte(`{"apiVersion":"hypershift.openshift.io/v1beta1","kind":"HostedCluster"}`)

	id1, _, err := buildApplyDesireDoc("cluster-abc", "mc-prod", ref, raw)
	require.NoError(t, err)
	id2, _, err := buildApplyDesireDoc("cluster-abc", "mc-prod", ref, raw)
	require.NoError(t, err)
	assert.Equal(t, id1, id2, "document ID must be deterministic")
	assert.NotEmpty(t, id1)
}

func TestBuildApplyDesireDoc_Contents(t *testing.T) {
	ref := kubeapplier.ResourceReference{
		Group: "hypershift.openshift.io", Version: "v1beta1",
		Resource: "hostedclusters", Namespace: "clusters-abc", Name: "my-hc",
	}
	raw := []byte(`{"apiVersion":"hypershift.openshift.io/v1beta1","kind":"HostedCluster","metadata":{"name":"my-hc"}}`)

	_, data, err := buildApplyDesireDoc("cluster-abc", "mc-prod", ref, raw)
	require.NoError(t, err)

	// Check top-level keys
	assert.Contains(t, data, "spec")
	assert.Contains(t, data, "status")
	assert.Contains(t, data, "spec_kubeContent")
	assert.NotContains(t, data, "manifestWorkName")

	// spec_kubeContent should be the parsed JSON map
	kubeContent, ok := data["spec_kubeContent"].(map[string]any)
	require.True(t, ok, "spec_kubeContent must be map[string]any")
	assert.Equal(t, "hypershift.openshift.io/v1beta1", kubeContent["apiVersion"])

	// spec sub-document
	specMap, ok := data["spec"].(kubeapplier.ApplyDesireSpec)
	require.True(t, ok, "spec must be ApplyDesireSpec")
	assert.Empty(t, specMap.ClusterID)
	assert.Empty(t, specMap.NodePoolName)
	assert.Equal(t, "cluster-abc", specMap.GroupKey)
	assert.Equal(t, "mc-prod", specMap.ManagementCluster)
	assert.Equal(t, ref, specMap.TargetItem)
	assert.Nil(t, specMap.KubeContent, "KubeContent in spec must be nil (stored in spec_kubeContent)")
}

func TestBuildApplyDesireDoc_InvalidJSON(t *testing.T) {
	_, _, err := buildApplyDesireDoc("cluster-abc", "mc-prod", kubeapplier.ResourceReference{}, []byte(`{`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal kubeContent")
}

func TestBuildReadDesireDoc(t *testing.T) {
	ref := kubeapplier.ResourceReference{
		Group: "hypershift.openshift.io", Version: "v1beta1",
		Resource: "hostedclusters", Namespace: "clusters-abc", Name: "my-hc",
	}
	docID, data := buildReadDesireDoc("cluster-abc", "mc-prod", ref)
	assert.NotEmpty(t, docID)
	assert.NotContains(t, data, "manifestWorkName")

	spec, ok := data["spec"].(kubeapplier.ReadDesireSpec)
	require.True(t, ok)
	assert.Empty(t, spec.ClusterID)
	assert.Empty(t, spec.NodePoolName)
	assert.Equal(t, "cluster-abc", spec.GroupKey)
	assert.Equal(t, ref, spec.TargetItem)
}

func TestBuildDeleteDesireDoc(t *testing.T) {
	ref := kubeapplier.ResourceReference{
		Group: "hypershift.openshift.io", Version: "v1beta1",
		Resource: "hostedclusters", Namespace: "clusters-abc", Name: "my-hc",
	}
	docID, data := buildDeleteDesireDoc("cluster-abc", "mc-prod", ref)
	assert.NotEmpty(t, docID)

	spec, ok := data["spec"].(kubeapplier.DeleteDesireSpec)
	require.True(t, ok)
	assert.Empty(t, spec.ClusterID)
	assert.Empty(t, spec.NodePoolName)
	assert.Equal(t, "cluster-abc", spec.GroupKey)
	assert.Equal(t, ref, spec.TargetItem)
}

func TestBuildApplyAndReadDesireDoc_SameDocumentID(t *testing.T) {
	// ApplyDesire and ReadDesire for the same resource must share the same
	// document ID so they're co-located and GetStatus can correlate them.
	ref := kubeapplier.ResourceReference{
		Group: "hypershift.openshift.io", Version: "v1beta1",
		Resource: "hostedclusters", Namespace: "clusters-abc", Name: "my-hc",
	}
	raw := []byte(`{"apiVersion":"hypershift.openshift.io/v1beta1","kind":"HostedCluster"}`)
	applyID, _, _ := buildApplyDesireDoc("cluster-abc", "mc-prod", ref, raw)
	readID, _ := buildReadDesireDoc("cluster-abc", "mc-prod", ref)
	assert.Equal(t, applyID, readID, "ApplyDesire and ReadDesire for the same resource must have the same document ID")
}
