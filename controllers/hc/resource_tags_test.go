package hc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/yaml"
)

func TestLoadResourceTags(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       []privatev1.GCPResourceTag
		wantErr    string
	}{
		{name: "configured", data: `[{"key":"redhat-managed","value":"true"},{"key":"environment","value":"prod","parentID":"123456789"}]`, want: []privatev1.GCPResourceTag{{Key: "redhat-managed", Value: "true"}, {Key: "environment", Value: "prod", ParentID: ptr.To("123456789")}}},
		{name: "empty", data: `[]`, want: []privatev1.GCPResourceTag{}},
		{name: "null", data: `null`},
		{name: "null parent", data: `[{"key":"env","value":"prod","parentID":null}]`, want: []privatev1.GCPResourceTag{{Key: "env", Value: "prod"}}},
		{name: "invalid JSON", data: `not-json`, wantErr: "parse resource tags"},
		{name: "object instead of list", data: `{"key":"x","value":"y"}`, wantErr: "parse resource tags"},
		{name: "missing key", data: `[{"value":"true"}]`, wantErr: "non-empty key and value"},
		{name: "missing value", data: `[{"key":"redhat-managed"}]`, wantErr: "non-empty key and value"},
		{name: "null entry", data: `[null]`, wantErr: "non-empty key and value"},
		{name: "non-string value", data: `[{"key":"x","value":true}]`, wantErr: "parse resource tags"},
		{name: "duplicate", data: `[{"key":"x","value":"y"},{"key":"x","value":"z"}]`, wantErr: "duplicate resource tag key"},
		{name: "too many", data: `[` + strings.Repeat(`{"key":"x","value":"y"},`, 50) + `{"key":"z","value":"y"}]`, wantErr: "at most 50"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tags, err := LoadResourceTags(writeTemp(t, tc.data))
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, tags)
		})
	}
	_, err := LoadResourceTags(filepath.Join(t.TempDir(), "missing.json"))
	require.ErrorContains(t, err, "read resource tags file")
}

func TestMergeResourceTags(t *testing.T) {
	requested := []privatev1.GCPResourceTag{
		{Key: "env", Value: "prod", ParentID: ptr.To("123456789")},
		{Key: "redhat-managed", Value: "false", ParentID: ptr.To("987654321")},
	}
	configured := []privatev1.GCPResourceTag{{Key: "redhat-managed", Value: "true"}, {Key: "app", Value: "platform", ParentID: ptr.To("987654321")}}
	requestedBefore := make([]privatev1.GCPResourceTag, len(requested))
	copy(requestedBefore, requested)
	configuredBefore := append([]privatev1.GCPResourceTag(nil), configured...)
	tags, err := mergeResourceTags(requested, configured, "my-project")
	require.NoError(t, err)
	require.Equal(t, []privatev1.GCPResourceTag{{Key: "app", Value: "platform", ParentID: ptr.To("987654321")}, {Key: "env", Value: "prod", ParentID: ptr.To("123456789")}, {Key: "redhat-managed", Value: "true"}}, tags)
	require.Equal(t, requestedBefore, requested)
	require.Equal(t, configuredBefore, configured)
	tags[0].Value = "changed"
	*tags[0].ParentID = "111111111"
	*tags[1].ParentID = "222222222"
	require.Equal(t, "987654321", *configured[1].ParentID, "merged tags must not alias configured parent IDs")
	require.Equal(t, "123456789", *requested[0].ParentID, "merged tags must not alias requested parent IDs")
	require.Equal(t, configuredBefore, configured)
	require.Equal(t, requestedBefore, requested)
	empty, err := mergeResourceTags(nil, nil, "my-project")
	require.NoError(t, err)
	require.Empty(t, empty)
	// Merge all desired tags; the transport checks the limit after preservation.
	many := make([]privatev1.GCPResourceTag, 50)
	for i := range many {
		many[i] = privatev1.GCPResourceTag{Key: fmt.Sprintf("tag%d", i), Value: "v"}
	}
	merged, err := mergeResourceTags(many, []privatev1.GCPResourceTag{{Key: "extra", Value: "v"}}, "my-project")
	require.NoError(t, err)
	require.Len(t, merged, 51)
	merged, err = mergeResourceTags(many, []privatev1.GCPResourceTag{{Key: "tag0", Value: "override"}}, "my-project")
	require.NoError(t, err)
	require.Len(t, merged, 50)
}

func TestLoadResourceTagsFormats(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tag     privatev1.GCPResourceTag
		wantErr string
	}{
		{name: "single characters", tag: privatev1.GCPResourceTag{Key: "A", Value: "1"}},
		{name: "maximum key and value", tag: privatev1.GCPResourceTag{Key: strings.Repeat("a", 63), Value: strings.Repeat("B", 63)}},
		{name: "key punctuation", tag: privatev1.GCPResourceTag{Key: "Env.Type_1-prod", Value: "true"}},
		{name: "value punctuation", tag: privatev1.GCPResourceTag{Key: "env", Value: "A _-.@%=+:,*#&()[]{} Z"}},
		{name: "explicit project", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To("my-project")}},
		{name: "shortest project", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To("a12345")}},
		{name: "longest project", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To("a" + strings.Repeat("b", 28) + "1")}},
		{name: "shortest organization", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To("1")}},
		{name: "longest organization", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To(strings.Repeat("9", 32))}},
		{name: "long key", tag: privatev1.GCPResourceTag{Key: strings.Repeat("a", 64), Value: "prod"}, wantErr: "key"},
		{name: "long value", tag: privatev1.GCPResourceTag{Key: "env", Value: strings.Repeat("a", 64)}, wantErr: "value"},
		{name: "key slash", tag: privatev1.GCPResourceTag{Key: "env/type", Value: "prod"}, wantErr: "key"},
		{name: "key whitespace", tag: privatev1.GCPResourceTag{Key: "env type", Value: "prod"}, wantErr: "key"},
		{name: "key leading punctuation", tag: privatev1.GCPResourceTag{Key: "-env", Value: "prod"}, wantErr: "key"},
		{name: "key trailing punctuation", tag: privatev1.GCPResourceTag{Key: "env_", Value: "prod"}, wantErr: "key"},
		{name: "key non-ASCII", tag: privatev1.GCPResourceTag{Key: "environmént", Value: "prod"}, wantErr: "key"},
		{name: "value slash", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod/test"}, wantErr: "value"},
		{name: "value leading whitespace", tag: privatev1.GCPResourceTag{Key: "env", Value: " prod"}, wantErr: "value"},
		{name: "value trailing punctuation", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod-"}, wantErr: "value"},
		{name: "value newline", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod\ntest"}, wantErr: "value"},
		{name: "value non-ASCII", tag: privatev1.GCPResourceTag{Key: "env", Value: "pröd"}, wantErr: "value"},
		{name: "empty parent", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To("")}, wantErr: "parentID"},
		{name: "organization zero", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To("0")}, wantErr: "parentID"},
		{name: "organization leading zero", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To("0123")}, wantErr: "parentID"},
		{name: "long organization", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To(strings.Repeat("9", 33))}, wantErr: "parentID"},
		{name: "organization prefix", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To("organizations/123")}, wantErr: "parentID"},
		{name: "short project", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To("abcde")}, wantErr: "parentID"},
		{name: "long project", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To(strings.Repeat("a", 31))}, wantErr: "parentID"},
		{name: "project uppercase", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To("My-project")}, wantErr: "parentID"},
		{name: "project trailing hyphen", tag: privatev1.GCPResourceTag{Key: "env", Value: "prod", ParentID: ptr.To("my-project-")}, wantErr: "parentID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal([]privatev1.GCPResourceTag{tc.tag})
			require.NoError(t, err)
			tags, err := LoadResourceTags(writeTemp(t, string(raw)))
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Nil(t, tags)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []privatev1.GCPResourceTag{tc.tag}, tags)
		})
	}
}

// The API schema mirrors HyperShift. Check its regexes and bounds explicitly
// so config validation cannot silently diverge, without adding a CEL runtime
// dependency to the controller test suite.
func TestResourceTagPatternsMatchAPISchema(t *testing.T) {
	var schema map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(privatev1.ClusterSchemaYAML), &schema))
	properties, found, err := unstructured.NestedMap(schema, "properties", "spec", "properties", "platform", "properties", "gcp", "properties", "resourceTags", "items", "properties")
	require.NoError(t, err)
	require.True(t, found)
	for _, tc := range []struct {
		field, pattern string
		maximum        int
	}{
		{"key", resourceTagKeyPattern.String(), 63},
		{"value", resourceTagValuePattern.String(), 63},
		{"parentID", resourceTagParentPattern.String(), 32},
	} {
		t.Run(tc.field, func(t *testing.T) {
			property := properties[tc.field].(map[string]any)
			require.EqualValues(t, 1, property["minLength"])
			require.EqualValues(t, tc.maximum, property["maxLength"])
			validations := property["x-kubernetes-validations"].([]any)
			require.Len(t, validations, 1)
			rule := validations[0].(map[string]any)["rule"].(string)
			require.True(t, strings.HasPrefix(rule, "self.matches('") && strings.HasSuffix(rule, "')"), "expected a single CEL regex rule: %s", rule)
			literal := strings.TrimSuffix(strings.TrimPrefix(rule, "self.matches('"), "')")
			// CEL string literals escape the RE2 expression, just as Go string literals do.
			pattern, err := strconv.Unquote(`"` + literal + `"`)
			require.NoError(t, err)
			require.Equal(t, pattern, tc.pattern, "config regex must match the generated API schema")
		})
	}
}

func TestMergeResourceTagsParentScope(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		requested, configured []privatev1.GCPResourceTag
		wantErr               bool
	}{
		{name: "omitted parent", configured: []privatev1.GCPResourceTag{{Key: "env", Value: "prod"}}},
		{name: "matching configured project", configured: []privatev1.GCPResourceTag{{Key: "env", Value: "prod", ParentID: ptr.To("my-project")}}},
		{name: "matching requested project", requested: []privatev1.GCPResourceTag{{Key: "env", Value: "prod", ParentID: ptr.To("my-project")}}},
		{name: "organization", configured: []privatev1.GCPResourceTag{{Key: "env", Value: "prod", ParentID: ptr.To("123456789")}}},
		{name: "other configured project", configured: []privatev1.GCPResourceTag{{Key: "env", Value: "prod", ParentID: ptr.To("other-project")}}, wantErr: true},
		{name: "other requested project", requested: []privatev1.GCPResourceTag{{Key: "env", Value: "prod", ParentID: ptr.To("other-project")}}, wantErr: true},
		{name: "override changes parent to valid organization", requested: []privatev1.GCPResourceTag{{Key: "env", Value: "prod", ParentID: ptr.To("my-project")}}, configured: []privatev1.GCPResourceTag{{Key: "env", Value: "prod", ParentID: ptr.To("123456789")}}},
		{name: "override changes parent to other project", requested: []privatev1.GCPResourceTag{{Key: "env", Value: "prod", ParentID: ptr.To("my-project")}}, configured: []privatev1.GCPResourceTag{{Key: "env", Value: "prod", ParentID: ptr.To("other-project")}}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tags, err := mergeResourceTags(tc.requested, tc.configured, "my-project")
			if tc.wantErr {
				require.ErrorContains(t, err, "parentID must be an organization ID or match the cluster GCP project ID")
				require.Nil(t, tags)
			} else {
				require.NoError(t, err)
				require.Len(t, tags, 1)
			}
		})
	}
}

func TestLoadResourceTagsErrorsRedactInput(t *testing.T) {
	const key = "customer-sensitive-key"
	const value = "customer-sensitive-value"
	for _, tc := range []struct{ name, data, want string }{
		{"duplicate", `[{"key":"` + key + `","value":"` + value + `"},{"key":"` + key + `","value":"other"}]`, `^duplicate resource tag key at index 1$`},
		{"syntax", `[{"key":"` + key + `","value":"` + value + `"}]!`, `^parse resource tags file: invalid JSON at byte [0-9]+$`},
		{"number instead of key", `[{"key":12345678901234567890,"value":"` + value + `"}]`, `^parse resource tags file: unexpected value type at byte [0-9]+$`},
		{"object instead of string", `[{"key":"` + key + `","value":{"` + value + `":"x"}}]`, `^parse resource tags file: unexpected value type at byte [0-9]+$`},
		{"object instead of list", `{"` + key + `":"` + value + `"}`, `^parse resource tags file: unexpected value type at byte [0-9]+$`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, tc.data)
			tags, err := LoadResourceTags(path)
			require.Error(t, err)
			require.Nil(t, tags)
			require.Regexp(t, tc.want, err.Error())
			for _, sensitive := range []string{path, filepath.Base(path), key, value, "12345678901234567890"} {
				require.NotContains(t, err.Error(), sensitive)
			}
		})
	}
}

func TestLoadResourceTagsReadErrorRedactsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "customer-sensitive-file.json")
	tags, err := LoadResourceTags(path)
	require.Error(t, err)
	require.Nil(t, tags)
	require.ErrorIs(t, err, os.ErrNotExist, "preserve filesystem error classification")
	require.ErrorContains(t, err, "read resource tags file:")
	require.NotContains(t, err.Error(), path)
	require.NotContains(t, err.Error(), filepath.Base(path))
}
