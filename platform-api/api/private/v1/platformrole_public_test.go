package v1_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/conversion"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
	publicv1 "github.com/openshift-online/gecko/platform-api/api/public/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/yaml"
)

func TestPlatformRolePublicProjection(t *testing.T) {
	privateScheme, publicScheme := runtime.NewScheme(), runtime.NewScheme()
	if err := privatev1.AddToScheme(privateScheme); err != nil {
		t.Fatal(err)
	}
	if err := publicv1.AddToScheme(publicScheme); err != nil {
		t.Fatal(err)
	}
	role := &privatev1.PlatformRole{
		TypeMeta: metav1.TypeMeta{APIVersion: privatev1.GroupVersion.String(), Kind: "PlatformRole"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "future-role", CreationTimestamp: metav1.NewTime(time.Now().Truncate(time.Second)),
			Annotations: map[string]string{"argocd.argoproj.io/tracking-id": "gitops", conversion.DefaultPrivatePrefix + "secret": "hidden"},
			Labels:      map[string]string{conversion.DefaultPrivatePrefix + "internal": "hidden"},
			Finalizers:  []string{"private-finalizer"},
		},
		Spec: privatev1.PlatformRoleSpec{Permissions: []string{"cluster.get", "cluster.list", "rolebinding.create"}},
	}
	original := role.DeepCopy()
	converted, err := conversion.NewConverter(publicScheme, privateScheme, "").PrivateToPublic(role)
	if err != nil {
		t.Fatal(err)
	}
	public := converted.(*publicv1.PlatformRole)
	if public.Name != role.Name || !public.CreationTimestamp.Equal(&role.CreationTimestamp) || !reflect.DeepEqual(public.Spec.Permissions, role.Spec.Permissions) {
		t.Fatalf("incomplete catalog projection: %#v", public)
	}
	if len(public.Labels) != 0 || len(public.Finalizers) != 0 || len(public.Annotations) != 1 || public.Annotations["argocd.argoproj.io/tracking-id"] != "gitops" {
		t.Fatalf("existing public metadata filtering changed: %#v", public.ObjectMeta)
	}
	public.Spec.Permissions[0] = "changed"
	if !reflect.DeepEqual(role, original) {
		t.Fatal("public conversion mutated private role")
	}
	copy := public.DeepCopy()
	copy.Spec.Permissions[0] = "copy"
	if public.Spec.Permissions[0] == "copy" {
		t.Fatal("public deep copy aliases permission slice")
	}
	list := &publicv1.PlatformRoleList{Items: []publicv1.PlatformRole{*public}}
	listCopy := list.DeepCopy()
	listCopy.Items[0].Spec.Permissions[0] = "list-copy"
	if list.Items[0].Spec.Permissions[0] == "list-copy" {
		t.Fatal("list deep copy aliases permissions")
	}
	data, err := json.Marshal(public.Spec)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || fields["permissions"] == nil {
		t.Fatalf("unexpected public fields: %s", data)
	}
}

func TestPlatformRoleSchemasExposeCompletePermissionSet(t *testing.T) {
	for name, data := range map[string]string{"private": privatev1.PlatformRoleSchemaYAML, "public": publicv1.PlatformRoleSchemaYAML} {
		t.Run(name, func(t *testing.T) {
			var schema map[string]any
			if err := yaml.Unmarshal([]byte(data), &schema); err != nil {
				t.Fatal(err)
			}
			spec := schema["properties"].(map[string]any)["spec"].(map[string]any)
			description, _ := spec["description"].(string)
			if !strings.HasPrefix(description, "spec ") {
				t.Fatalf("missing customer-facing spec description: %q", description)
			}
			properties := spec["properties"].(map[string]any)
			if len(properties) != 1 {
				t.Fatalf("unexpected spec fields: %v", properties)
			}
			permissions := properties["permissions"].(map[string]any)
			description, _ = permissions["description"].(string)
			if !strings.HasPrefix(description, "permissions ") {
				t.Fatalf("missing customer-facing permissions description: %q", description)
			}
			if permissions["type"] != "array" || permissions["x-kubernetes-list-type"] != "set" {
				t.Fatalf("invalid permission schema: %v", permissions)
			}
		})
	}
}
