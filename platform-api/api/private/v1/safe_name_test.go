package v1

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestSafeNameFromClusterName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "safe lowercase", in: "my-cluster", want: "my-cluster"},
		{name: "uppercase", in: "My-Cluster", want: "my-cluster"},
		{name: "invalid characters", in: "my.cluster_test", want: "my-cluster-test"},
		{name: "repeated hyphens", in: "my---cluster", want: "my-cluster"},
		{name: "leading trailing hyphens", in: "---my-cluster---", want: "my-cluster"},
		{name: "truncates", in: "very-long-cluster-name", want: "very-long-cluster"},
		{name: "trims trailing hyphen after truncation", in: "very-long-cluster-name", want: "very-long-cluster"},
		{name: "empty after sanitization", in: "...", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SafeNameFromClusterName(tt.in); got != tt.want {
				t.Fatalf("SafeNameFromClusterName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSafeNameFromClusterName_MaxLength(t *testing.T) {
	got := SafeNameFromClusterName(strings.Repeat("a", MaxSafeNameLength+1))
	if len(got) != MaxSafeNameLength {
		t.Fatalf("safe name length = %d, want %d", len(got), MaxSafeNameLength)
	}
}

func TestDefaultSafeName_Fallback(t *testing.T) {
	uid := types.UID("550e8400-e29b-41d4-a716-446655440000")
	got := DefaultSafeName("...", uid)
	want := "hc-550e8400e29b41"
	if got != want {
		t.Fatalf("DefaultSafeName fallback = %q, want %q", got, want)
	}
	if len(got) > MaxSafeNameLength {
		t.Fatalf("safe name length = %d, want <= %d", len(got), MaxSafeNameLength)
	}
}

func TestClusterDefault_SetsSafeName(t *testing.T) {
	cluster := &Cluster{}
	cluster.SetName("My.Cluster")
	cluster.SetUID(types.UID("550e8400-e29b-41d4-a716-446655440000"))

	if err := cluster.Default(t.Context()); err != nil {
		t.Fatalf("Default() error = %v", err)
	}
	if cluster.Spec.SafeName != "my-cluster" {
		t.Fatalf("safeName = %q, want %q", cluster.Spec.SafeName, "my-cluster")
	}
}

func TestClusterValidateUpdate_SafeNameImmutable(t *testing.T) {
	oldCluster := &Cluster{}
	oldCluster.SetName("old-name")
	oldCluster.SetUID(types.UID("550e8400-e29b-41d4-a716-446655440000"))
	oldCluster.Spec.SafeName = "old-name"

	newCluster := oldCluster.DeepCopy()
	newCluster.Spec.SafeName = "new-name"

	if err := newCluster.ValidateUpdate(t.Context(), oldCluster); err == nil {
		t.Fatal("expected safeName immutability error")
	}
}

func TestClusterValidateUpdate_AllowsBackfillWhenOldSafeNameEmpty(t *testing.T) {
	oldCluster := &Cluster{}
	oldCluster.SetName("new-name")
	oldCluster.SetUID(types.UID("550e8400-e29b-41d4-a716-446655440000"))
	newCluster := &Cluster{}
	newCluster.SetName("new-name")
	newCluster.SetUID(types.UID("550e8400-e29b-41d4-a716-446655440000"))
	newCluster.Spec.SafeName = "new-name"

	if err := newCluster.ValidateUpdate(t.Context(), oldCluster); err != nil {
		t.Fatalf("ValidateUpdate() error = %v", err)
	}
}

func TestClusterValidateCreate_RequiresDefaultSafeName(t *testing.T) {
	cluster := &Cluster{}
	cluster.SetName("cluster-name")
	cluster.SetUID(types.UID("550e8400-e29b-41d4-a716-446655440000"))
	cluster.Spec.SafeName = "wrong-name"

	if err := cluster.ValidateCreate(t.Context()); err == nil {
		t.Fatal("expected safeName validation error")
	}
}
