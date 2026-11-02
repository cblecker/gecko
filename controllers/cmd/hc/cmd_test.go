package hc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openshift-online/gecko/controllers/util/setup"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestNodePoolToCluster(t *testing.T) {
	nodePool := &privatev1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "nodepool"},
		Spec:       privatev1.NodePoolSpec{ClusterID: "cluster"},
	}

	requests := nodePoolToCluster(context.Background(), nodePool)
	require.Len(t, requests, 1)
	require.Equal(t, "test", requests[0].Namespace)
	require.Equal(t, "cluster", requests[0].Name)
	require.Empty(t, nodePoolToCluster(context.Background(), &privatev1.NodePool{}))
}

func TestNodePoolClusterID(t *testing.T) {
	require.Equal(t, []string{"cluster"}, nodePoolClusterID(&privatev1.NodePool{
		Spec: privatev1.NodePoolSpec{ClusterID: "cluster"},
	}))
	require.Empty(t, nodePoolClusterID(&privatev1.NodePool{}))
}

func TestNodePoolDeletePredicate(t *testing.T) {
	predicate := nodePoolDeletePredicate()
	nodePool := &privatev1.NodePool{}

	require.False(t, predicate.Create(event.CreateEvent{Object: nodePool}))
	require.False(t, predicate.Update(event.UpdateEvent{ObjectOld: nodePool, ObjectNew: nodePool}))
	require.False(t, predicate.Generic(event.GenericEvent{Object: nodePool}))
	require.True(t, predicate.Delete(event.DeleteEvent{Object: nodePool}))
}

func TestResourceTagsFlagFailsStartupOnInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resource-tags.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"not":"a tag list"}`), 0o600))
	cmd := NewCommand(&setup.RootFlags{LogLevel: "error", LogFormat: "json"})
	cmd.SetArgs([]string{"--resource-tags-file=" + path})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	require.ErrorContains(t, cmd.ExecuteContext(t.Context()), "load resource tags: parse resource tags file")
}

func TestResourceTagsStartupErrorsRedactInput(t *testing.T) {
	for _, tc := range []struct {
		name, data, want string
		missing          bool
	}{
		{name: "duplicate", data: `[{"key":"customer-private-key","value":"customer-private-value"},{"key":"customer-private-key","value":"other"}]`, want: "load resource tags: duplicate resource tag key at index 1"},
		{name: "parse", data: `[{"key":12345678901234567890,"value":"customer-private-value"}]`, want: "load resource tags: parse resource tags file: unexpected value type at byte"},
		{name: "read", missing: true, want: "load resource tags: read resource tags file:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "customer-private-path.json")
			if !tc.missing {
				require.NoError(t, os.WriteFile(path, []byte(tc.data), 0o600))
			}
			cmd := NewCommand(&setup.RootFlags{LogLevel: "error", LogFormat: "json"})
			var stderr bytes.Buffer
			cmd.SetErr(&stderr)
			cmd.SetArgs([]string{"--resource-tags-file=" + path})
			cmd.SilenceUsage = true
			err := cmd.ExecuteContext(t.Context())
			require.ErrorContains(t, err, tc.want)
			require.Contains(t, stderr.String(), tc.want)
			if tc.missing {
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			for _, output := range []string{err.Error(), stderr.String()} {
				for _, sensitive := range []string{path, filepath.Base(path), "customer-private-key", "customer-private-value", "12345678901234567890"} {
					require.NotContains(t, output, sensitive)
				}
			}
		})
	}
}
