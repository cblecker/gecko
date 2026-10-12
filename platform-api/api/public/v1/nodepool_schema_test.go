package v1

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNodePoolSchemaChannelGroup(t *testing.T) {
	processor := newSchemaProcessor(t, NodePoolSchemaYAML)
	for _, group := range []string{"stable", "fast", "candidate", "eus", "nightly", "dev-preview", "a", strings.Repeat("a", 63)} {
		t.Run("accept-"+group, func(t *testing.T) {
			if errs := processor.Process(context.Background(), nodePoolAsMap(t, group), nil); len(errs) > 0 {
				t.Fatalf("expected channelGroup %q to pass schema validation, got: %v", group, errs)
			}
		})
	}
	for _, group := range []string{"", "Nightly", "-nightly", "nightly-", "nightly_x", "nightly.4", strings.Repeat("a", 64)} {
		t.Run("reject-"+group, func(t *testing.T) {
			errs := processor.Process(context.Background(), nodePoolAsMap(t, group), nil)
			if len(errs) == 0 || !strings.Contains(errs.ToAggregate().Error(), "channelGroup") {
				t.Fatalf("expected channelGroup %q to fail schema validation, got: %v", group, errs)
			}
		})
	}
}

func nodePoolAsMap(t *testing.T, group string) map[string]any {
	t.Helper()
	return objectAsMap(t, NodePool{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "NodePool"},
		ObjectMeta: metav1.ObjectMeta{Name: "test-nodepool", Namespace: "test-namespace"},
		Spec: NodePoolSpec{
			ClusterID: "test-cluster",
			Platform: NodePoolPlatformSpec{
				Type: "GCP",
				GCP:  &GCPNodePoolPlatform{MachineType: "n2-standard-4", Zone: "us-central1-a", Subnet: "test-subnet"},
			},
			Release: ReleaseSpec{Version: "5.0.0-ec.6", ChannelGroup: group},
		},
	})
}
