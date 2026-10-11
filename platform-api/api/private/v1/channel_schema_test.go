package v1_test

import (
	"context"
	"encoding/json"
	"testing"

	pkgschema "github.com/openshift-online/gecko/orlop/pkg/apiserver/schema"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
	publicv1 "github.com/openshift-online/gecko/platform-api/api/public/v1"

	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/yaml"
)

func TestChannelSchemaMinimumSupportedVersion(t *testing.T) {
	for _, tc := range []struct {
		minimum string
		want    string
		invalid bool
	}{
		{minimum: "4.21", want: "4.21"},
		{minimum: "4.23", want: "4.23"},
		{minimum: "4.24", want: "4.24"},
		{minimum: "", invalid: true},
		{minimum: "5", invalid: true},
		{minimum: "4.24.0", invalid: true},
		{minimum: "04.24", invalid: true},
		{minimum: "4.024", invalid: true},
		{minimum: "-1.0", invalid: true},
		{minimum: "5.-1", invalid: true},
		{minimum: "v4.24", invalid: true},
	} {
		t.Run(tc.minimum, func(t *testing.T) {
			channel := privatev1.Channel{
				TypeMeta:   metav1.TypeMeta{APIVersion: privatev1.GroupVersion.String(), Kind: "Channel"},
				ObjectMeta: metav1.ObjectMeta{Name: "stable"},
				Spec: privatev1.ChannelSpec{
					MinimumSupportedVersion: tc.minimum,
					InstallDefaultVersion:   "4.22.11",
					FleetMinorVersion:       "4.23",
				},
			}
			data, err := json.Marshal(channel)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err := json.Unmarshal(data, &object); err != nil {
				t.Fatal(err)
			}
			processor := newChannelSchemaProcessor(t, privatev1.ChannelSchemaYAML)
			errs := processor.Process(context.Background(), object, nil)
			if tc.invalid {
				if len(errs) == 0 {
					t.Fatalf("expected minimum %q to fail schema validation", tc.minimum)
				}
				return
			}
			if len(errs) > 0 {
				t.Fatalf("expected minimum %q to pass schema validation, got: %v", tc.minimum, errs)
			}
			spec := object["spec"].(map[string]any)
			if got := spec["minimumSupportedVersion"]; got != tc.want {
				t.Fatalf("minimumSupportedVersion = %v, want %q", got, tc.want)
			}
		})
	}
}

func TestChannelPublicSpecOnlyExposesInstallDefault(t *testing.T) {
	channel := privatev1.Channel{
		ObjectMeta: metav1.ObjectMeta{Name: "stable"},
		Spec: privatev1.ChannelSpec{
			MinimumSupportedVersion: "4.22",
			InstallDefaultVersion:   "4.22.11",
			FleetMinorVersion:       "4.23",
		},
	}
	var public publicv1.Channel
	if err := publicv1.Convert_Channel_PrivateToPublic(&channel, &public, nil); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(public.Spec)
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec) != 1 || spec["installDefaultVersion"] != "4.22.11" {
		t.Fatalf("unexpected public Channel spec: %s", data)
	}
	var schema apiextv1.JSONSchemaProps
	if err := yaml.Unmarshal([]byte(publicv1.ChannelSchemaYAML), &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema.Properties["spec"].Properties
	if len(properties) != 1 {
		t.Fatalf("expected only installDefaultVersion in public Channel schema, got: %v", properties)
	}
	if _, ok := properties["installDefaultVersion"]; !ok {
		t.Fatal("public Channel schema must expose installDefaultVersion")
	}
}

func newChannelSchemaProcessor(t *testing.T, schemaYAML string) *pkgschema.Processor {
	t.Helper()
	var propsV1 apiextv1.JSONSchemaProps
	if err := yaml.Unmarshal([]byte(schemaYAML), &propsV1); err != nil {
		t.Fatalf("unmarshal generated Channel schema: %v", err)
	}
	var props apiext.JSONSchemaProps
	if err := apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&propsV1, &props, nil); err != nil {
		t.Fatalf("convert generated Channel schema: %v", err)
	}
	structural, err := structuralschema.NewStructural(&props)
	if err != nil {
		t.Fatalf("create structural Channel schema: %v", err)
	}
	processor, err := pkgschema.NewProcessor(structural, &props)
	if err != nil {
		t.Fatalf("create Channel schema processor: %v", err)
	}
	return processor
}
