package v1

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	pkgschema "github.com/openshift-online/gecko/orlop/pkg/apiserver/schema"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func TestClusterSchemaAcceptsPrereleaseVersion(t *testing.T) {
	processor := newClusterSchemaProcessor(t)
	object := clusterAsMap(t, validCluster("5.0.0-ec.6"))

	if errs := processor.Process(context.Background(), object, nil); len(errs) > 0 {
		t.Fatalf("expected prerelease cluster request to pass schema validation, got: %v", errs)
	}
}

func TestClusterSchemaRejectsInvalidReleaseVersions(t *testing.T) {
	for _, version := range []string{"v5.0.0", "5.0.0-01"} {
		t.Run(version, func(t *testing.T) {
			processor := newClusterSchemaProcessor(t)
			errs := processor.Process(context.Background(), clusterAsMap(t, validCluster(version)), nil)
			if len(errs) == 0 {
				t.Fatalf("expected version %q to fail schema validation", version)
			}
			if !strings.Contains(errs.ToAggregate().Error(), "version must be a valid semantic version") {
				t.Fatalf("expected version validation error, got: %v", errs)
			}
		})
	}
}

func validCluster(version string) Cluster {
	return Cluster{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GroupVersion.String(),
			Kind:       "Cluster",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
		Spec: ClusterSpec{
			InfraID: "test-cluster",
			Platform: ClusterPlatformSpec{
				Type: "GCP",
				GCP: &GCPClusterPlatform{
					ProjectID: "test-project",
					Region:    "us-central1",
					Network:   "test-network",
					Subnet:    "test-subnet",
					WorkloadIdentity: WorkloadIdentitySpec{
						PoolID:        "test-pool",
						ProjectNumber: "123456789",
						ProviderID:    "test-provider",
						ServiceAccountsRef: &ServiceAccountsRef{
							NodePoolEmail:        "nodepool@test-project.iam.gserviceaccount.com",
							ControlPlaneEmail:    "control-plane@test-project.iam.gserviceaccount.com",
							CloudControllerEmail: "cloud-controller@test-project.iam.gserviceaccount.com",
							StorageEmail:         "storage@test-project.iam.gserviceaccount.com",
							ImageRegistryEmail:   "image-registry@test-project.iam.gserviceaccount.com",
							NetworkEmail:         "network@test-project.iam.gserviceaccount.com",
						},
					},
				},
			},
			Release: ReleaseSpec{
				Version:      version,
				ChannelGroup: "candidate",
			},
			Networking: NetworkingSpec{},
		},
	}
}

func newClusterSchemaProcessor(t *testing.T) *pkgschema.Processor {
	t.Helper()

	var propsV1 apiextv1.JSONSchemaProps
	if err := yaml.Unmarshal([]byte(ClusterSchemaYAML), &propsV1); err != nil {
		t.Fatalf("unmarshal generated Cluster schema: %v", err)
	}

	var props apiext.JSONSchemaProps
	if err := apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&propsV1, &props, nil); err != nil {
		t.Fatalf("convert generated Cluster schema: %v", err)
	}

	structural, err := structuralschema.NewStructural(&props)
	if err != nil {
		t.Fatalf("create structural Cluster schema: %v", err)
	}

	processor, err := pkgschema.NewProcessor(structural, &props)
	if err != nil {
		t.Fatalf("create Cluster schema processor: %v", err)
	}

	return processor
}

func clusterAsMap(t *testing.T, cluster Cluster) map[string]any {
	t.Helper()

	data, err := json.Marshal(cluster)
	if err != nil {
		t.Fatalf("marshal Cluster: %v", err)
	}

	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("unmarshal Cluster JSON: %v", err)
	}

	return object
}
