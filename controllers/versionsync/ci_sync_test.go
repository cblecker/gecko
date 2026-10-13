package versionsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type ciTag struct {
	Name     string `json:"name"`
	Phase    string `json:"phase"`
	PullSpec string `json:"pullSpec"`
}

func accepted(version string) ciTag {
	return ciTag{Name: version, Phase: "Accepted", PullSpec: "registry.ci.openshift.org/ocp/release:" + version}
}
func ciChannel(name, defaultVersion, fleetMinor string) *privatev1.Channel {
	return &privatev1.Channel{ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 3}, Spec: privatev1.ChannelSpec{MinimumSupportedVersion: fleetMinor, InstallDefaultVersion: defaultVersion, FleetMinorVersion: fleetMinor}}
}
func ciTestController(t *testing.T, store client.Client, responses map[string][]ciTag) *Controller {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/releasestreams/all" {
			index := make(map[string][]string, len(responses))
			for name := range responses {
				index[name] = []string{}
			}
			_ = json.NewEncoder(w).Encode(index)
			return
		}
		for stream, tags := range responses {
			if r.URL.Path == "/api/v1/releasestream/"+stream+"/tags" {
				_ = json.NewEncoder(w).Encode(struct {
					Name string  `json:"name"`
					Tags []ciTag `json:"tags"`
				}{stream, tags})
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	source, err := NewReleaseControllerClient(server.URL)
	require.NoError(t, err)
	return NewCIController(source, newTestLogger(t), store)
}
func ciStore(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, privatev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&privatev1.Channel{}).WithObjects(objects...).Build()
}
func readChannel(t *testing.T, store client.Client, name string) *privatev1.Channel {
	t.Helper()
	channel := &privatev1.Channel{}
	require.NoError(t, store.Get(context.Background(), client.ObjectKey{Name: name}, channel))
	return channel
}
func TestCISyncCatalogAndDefaults(t *testing.T) {
	ctx := context.Background()
	nightly := "4.23.0-0.nightly-2026-10-04-051631"
	future := "5.0.0-0.nightly-2026-10-04-051631"
	store := ciStore(t,
		ciChannel("stable", "4.23.1", "4.23"),
		ciChannel("nightly", "4.23.1", "4.23"),
		ciChannel("custom", future, "5.0"),
		&privatev1.Version{ObjectMeta: metav1.ObjectMeta{Name: "4.23.9"}, Spec: privatev1.VersionSpec{ReleaseImage: "old", ChannelGroups: []string{"removed"}}},
	)
	responses := map[string][]ciTag{
		"4-stable":                 {accepted("4.23.1"), accepted("4.23.2"), accepted("4.22.9")},
		"5-stable":                 {accepted("5.0.0-rc.1")},
		"4.23.0-0.nightly":         {accepted(nightly)},
		"5.0.0-0.nightly":          {accepted(future), {Name: "5.0.1", Phase: "Rejected", PullSpec: "rejected"}},
		"5-custom":                 {accepted(future), accepted("4.23.2")},
		"5.0.0-0.nightly-art23398": {accepted("5.0.2")},
	}
	controller := ciTestController(t, store, responses)
	controller.sync(ctx, newTestLogger(t))
	var versions privatev1.VersionList
	require.NoError(t, store.List(ctx, &versions))
	require.Len(t, versions.Items, 5)
	for _, version := range versions.Items {
		switch version.Name {
		case future:
			assert.Equal(t, []string{"custom", "nightly"}, version.Spec.ChannelGroups)
		case nightly:
			assert.Equal(t, []string{"nightly"}, version.Spec.ChannelGroups)
		default:
			assert.Equal(t, []string{"stable"}, version.Spec.ChannelGroups)
		}
	}
	stable := readChannel(t, store, "stable")
	condition := meta.FindStatusCondition(stable.Status.Conditions, privatev1.ChannelDefaultVersionAvailable)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionTrue, condition.Status)
	assert.EqualValues(t, 3, condition.ObservedGeneration)
	missing := readChannel(t, store, "nightly")
	condition = meta.FindStatusCondition(missing.Status.Conditions, privatev1.ChannelDefaultVersionAvailable)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, "4.23.1", missing.Spec.InstallDefaultVersion)
	controller.sync(ctx, newTestLogger(t))
	assert.Equal(t, stable, readChannel(t, store, "stable"))
	assert.Equal(t, missing, readChannel(t, store, "nightly"))
	var repeated privatev1.VersionList
	require.NoError(t, store.List(ctx, &repeated))
	assert.Equal(t, versions.Items, repeated.Items)
	responses["4.23.0-0.nightly"] = append(responses["4.23.0-0.nightly"], accepted("4.23.1"))
	controller.sync(ctx, newTestLogger(t))
	assert.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(readChannel(t, store, "nightly").Status.Conditions, privatev1.ChannelDefaultVersionAvailable).Status)
}

func TestCIFailedSnapshotPreservesVersions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses map[string][]ciTag
	}{
		{"no streams", nil},
		{"no matching stream", map[string][]ciTag{"4-other": {accepted("4.22.2")}}},
		{"only variant stream", map[string][]ciTag{"4.22.0-0.test-variant": {accepted("4.22.2")}}},
		{"empty catalog", map[string][]ciTag{"4-test": {}}},
		{"below minimum only", map[string][]ciTag{"4-test": {accepted("4.21.1")}}},
		{"missing payload", map[string][]ciTag{"4-test": {{Name: "4.22.2", Phase: "Accepted"}}}},
		{"conflicting payloads", map[string][]ciTag{"4-test": {accepted("4.22.1")}, "4.22.0-0.test": {{Name: "4.22.1", Phase: "Accepted", PullSpec: "different"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := &privatev1.Version{ObjectMeta: metav1.ObjectMeta{Name: "4.22.1"}, Spec: privatev1.VersionSpec{ReleaseImage: "old", ChannelGroups: []string{"test"}}}
			store := ciStore(t, old, ciChannel("test", "4.22.1", "4.22"))
			controller := ciTestController(t, store, tc.responses)
			controller.sync(context.Background(), newTestLogger(t))
			var versions privatev1.VersionList
			require.NoError(t, store.List(context.Background(), &versions))
			require.Len(t, versions.Items, 1)
			assert.Equal(t, old.Spec, versions.Items[0].Spec)
			condition := meta.FindStatusCondition(readChannel(t, store, "test").Status.Conditions, privatev1.ChannelDefaultVersionAvailable)
			require.NotNil(t, condition)
			assert.Equal(t, metav1.ConditionUnknown, condition.Status)
			assert.Equal(t, "FetchFailed", condition.Reason)
		})
	}
}

func TestCIEmptyStreamDoesNotBlockOtherChannels(t *testing.T) {
	store := ciStore(t, ciChannel("empty", "4.22.1", "4.22"), ciChannel("good", "4.22.2", "4.22"))
	controller := ciTestController(t, store, map[string][]ciTag{"4-empty": {}, "4-good": {accepted("4.22.2")}})
	controller.sync(context.Background(), newTestLogger(t))
	assert.Equal(t, metav1.ConditionFalse, meta.FindStatusCondition(readChannel(t, store, "empty").Status.Conditions, privatev1.ChannelDefaultVersionAvailable).Status)
	assert.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(readChannel(t, store, "good").Status.Conditions, privatev1.ChannelDefaultVersionAvailable).Status)
}

type failingVersionCreate struct{ client.Client }

func (c failingVersionCreate) Create(context.Context, client.Object, ...client.CreateOption) error {
	return fmt.Errorf("write unavailable")
}
func TestCIApplyFailureReportsUnknown(t *testing.T) {
	store := ciStore(t, ciChannel("test", "4.22.2", "4.22"), &privatev1.Version{ObjectMeta: metav1.ObjectMeta{Name: "4.22.1"}})
	controller := ciTestController(t, failingVersionCreate{store}, map[string][]ciTag{"4-test": {accepted("4.22.2")}})
	controller.sync(context.Background(), newTestLogger(t))
	condition := meta.FindStatusCondition(readChannel(t, store, "test").Status.Conditions, privatev1.ChannelDefaultVersionAvailable)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionUnknown, condition.Status)
	assert.Equal(t, "ApplyFailed", condition.Reason)
	var old privatev1.Version
	require.NoError(t, store.Get(context.Background(), client.ObjectKey{Name: "4.22.1"}, &old))
}

// A status outage must not block catalog updates, and the next sync must retry it.
type failingStatusClient struct{ client.Client }
type failingStatusWriter struct{ client.SubResourceWriter }

func (c failingStatusClient) Status() client.SubResourceWriter {
	return failingStatusWriter{c.Client.Status()}
}
func (w failingStatusWriter) Update(context.Context, client.Object, ...client.SubResourceUpdateOption) error {
	return fmt.Errorf("status unavailable")
}
func TestCIStatusFailureRetriesWithoutBlockingCatalog(t *testing.T) {
	store := ciStore(t, ciChannel("test", "4.22.1", "4.22"))
	controller := ciTestController(t, failingStatusClient{store}, map[string][]ciTag{"4-test": {accepted("4.22.1")}})
	controller.sync(context.Background(), newTestLogger(t))
	var version privatev1.Version
	require.NoError(t, store.Get(context.Background(), client.ObjectKey{Name: "4.22.1"}, &version))
	assert.Empty(t, readChannel(t, store, "test").Status.Conditions)
	controller.apiClient = store
	controller.sync(context.Background(), newTestLogger(t))
	assert.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(readChannel(t, store, "test").Status.Conditions, privatev1.ChannelDefaultVersionAvailable).Status)
}

func TestCICatalogChangesRemoveStaleMemberships(t *testing.T) {
	store := ciStore(t, ciChannel("one", "4.22.1", "4.22"), ciChannel("two", "4.22.1", "4.22"))
	responses := map[string][]ciTag{"4-one": {accepted("4.22.1"), accepted("4.22.2")}, "4-two": {accepted("4.22.1")}}
	controller := ciTestController(t, store, responses)
	controller.sync(context.Background(), newTestLogger(t))
	responses["4-one"] = []ciTag{}
	controller.sync(context.Background(), newTestLogger(t))
	var versions privatev1.VersionList
	require.NoError(t, store.List(context.Background(), &versions))
	require.Len(t, versions.Items, 1)
	assert.Equal(t, "4.22.1", versions.Items[0].Name)
	assert.Equal(t, []string{"two"}, versions.Items[0].Spec.ChannelGroups)
	assert.Equal(t, metav1.ConditionFalse, meta.FindStatusCondition(readChannel(t, store, "one").Status.Conditions, privatev1.ChannelDefaultVersionAvailable).Status)
}

func TestCIMissingChannelPreservesWholeSnapshot(t *testing.T) {
	old := &privatev1.Version{ObjectMeta: metav1.ObjectMeta{Name: "4.23.1"}, Spec: privatev1.VersionSpec{ReleaseImage: "old", ChannelGroups: []string{"stable", "z-missing"}}}
	store := ciStore(t, old, ciChannel("stable", "4.23.1", "4.23"), ciChannel("z-missing", "4.23.1", "4.23"))
	controller := ciTestController(t, store, map[string][]ciTag{"4-stable": {accepted("4.23.2")}})
	controller.sync(context.Background(), newTestLogger(t))
	var versions privatev1.VersionList
	require.NoError(t, store.List(context.Background(), &versions))
	require.Len(t, versions.Items, 1)
	assert.Equal(t, old.Spec, versions.Items[0].Spec)
	for _, name := range []string{"stable", "z-missing"} {
		assert.Equal(t, metav1.ConditionUnknown, meta.FindStatusCondition(readChannel(t, store, name).Status.Conditions, privatev1.ChannelDefaultVersionAvailable).Status)
	}
	_, err := controller.fetchCIVersions(context.Background(), []privatev1.Channel{*ciChannel("z-missing", "4.23.1", "4.23")})
	require.ErrorContains(t, err, `channel "z-missing" has no matching CI streams`)
}

func TestCIDiscoversNewMajorWithoutChannelChanges(t *testing.T) {
	channel := ciChannel("stable", "4.23.1", "4.23")
	store := ciStore(t, channel)
	responses := map[string][]ciTag{"4-stable": {accepted("4.23.1")}}
	controller := ciTestController(t, store, responses)
	controller.sync(context.Background(), newTestLogger(t))
	responses["5-stable"] = []ciTag{accepted("5.0.0")}
	controller.sync(context.Background(), newTestLogger(t))
	var versions privatev1.VersionList
	require.NoError(t, store.List(context.Background(), &versions))
	require.Len(t, versions.Items, 2)
	assert.Equal(t, channel.Spec, readChannel(t, store, "stable").Spec)
}

func TestCIUsesConfiguredMinimumBelowOldHardCodedFloor(t *testing.T) {
	store := ciStore(t, ciChannel("stable", "4.21.1", "4.21"))
	controller := ciTestController(t, store, map[string][]ciTag{"4-stable": {accepted("4.20.1"), accepted("4.21.1")}})
	controller.sync(context.Background(), newTestLogger(t))
	var versions privatev1.VersionList
	require.NoError(t, store.List(context.Background(), &versions))
	require.Len(t, versions.Items, 1)
	assert.Equal(t, "4.21.1", versions.Items[0].Name)
}

func TestCIMinimumSupportedVersionIsIndependentOfFleet(t *testing.T) {
	channel := ciChannel("nightly", "4.22.1", "4.22")
	channel.Spec.FleetMinorVersion = "4.24"
	store := ciStore(t, channel)
	controller := ciTestController(t, store, map[string][]ciTag{
		"4.22.0-0.nightly": {accepted("4.22.1")},
	})
	controller.sync(context.Background(), newTestLogger(t))
	var versions privatev1.VersionList
	require.NoError(t, store.List(context.Background(), &versions))
	require.Len(t, versions.Items, 1)
	assert.Equal(t, "4.22.1", versions.Items[0].Name)
	assert.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(readChannel(t, store, "nightly").Status.Conditions, privatev1.ChannelDefaultVersionAvailable).Status)
}

func TestCIIndexAndTagFailuresPreserveCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, indexBody, tagBody string
		indexStatus, tagStatus   int
	}{
		{"index outage", "", "", 503, 200},
		{"malformed index", "{", "", 200, 200},
		{"disappearing stream", `{"4-stable":[],"5-stable":[]}`, "", 200, 404},
		{"malformed tags", `{"4-stable":[],"5-stable":[]}`, "{", 200, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/releasestreams/all":
					w.WriteHeader(tc.indexStatus)
					fmt.Fprint(w, tc.indexBody)
				case "/api/v1/releasestream/4-stable/tags":
					fmt.Fprint(w, `{"name":"4-stable","tags":[{"name":"4.23.2","phase":"Accepted","pullSpec":"new"}]}`)
				default:
					w.WriteHeader(tc.tagStatus)
					fmt.Fprint(w, tc.tagBody)
				}
			}))
			defer server.Close()
			source, err := NewReleaseControllerClient(server.URL)
			require.NoError(t, err)
			old := &privatev1.Version{ObjectMeta: metav1.ObjectMeta{Name: "4.23.1"}, Spec: privatev1.VersionSpec{ReleaseImage: "old", ChannelGroups: []string{"stable"}}}
			store := ciStore(t, old, ciChannel("stable", "4.23.1", "4.23"))
			NewCIController(source, newTestLogger(t), store).sync(context.Background(), newTestLogger(t))
			var versions privatev1.VersionList
			require.NoError(t, store.List(context.Background(), &versions))
			require.Len(t, versions.Items, 1)
			assert.Equal(t, old.Spec, versions.Items[0].Spec)
			assert.Equal(t, metav1.ConditionUnknown, meta.FindStatusCondition(readChannel(t, store, "stable").Status.Conditions, privatev1.ChannelDefaultVersionAvailable).Status)
		})
	}
}

func TestCIDiscoversOnceAndFetchesOnlyEligibleStreams(t *testing.T) {
	indexRequests := 0
	tagRequests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/releasestreams/all" {
			indexRequests++
			fmt.Fprint(w, `{"4-stable":[],"5-stable":[],"4.22.0-0.nightly":[],"4.23.0-0.nightly":[],"5.0.0-0.nightly":[],"5.0.0-0.nightly-art23398":[]}`)
			return
		}
		tagRequests = append(tagRequests, r.URL.Path)
		for _, stream := range []string{"5-stable", "4.23.0-0.nightly", "5.0.0-0.nightly"} {
			if r.URL.Path == "/api/v1/releasestream/"+stream+"/tags" {
				assert.Equal(t, "Accepted", r.URL.Query().Get("phase"))
				_ = json.NewEncoder(w).Encode(struct {
					Name string  `json:"name"`
					Tags []ciTag `json:"tags"`
				}{stream, []ciTag{accepted("5.0.0")}})
				return
			}
		}
		http.Error(w, "unexpected stream", http.StatusInternalServerError)
	}))
	defer server.Close()
	source, err := NewReleaseControllerClient(server.URL)
	require.NoError(t, err)
	store := ciStore(t, ciChannel("stable", "5.0.0", "5.0"), ciChannel("nightly", "5.0.0", "4.23"))
	NewCIController(source, newTestLogger(t), store).sync(context.Background(), newTestLogger(t))
	assert.Equal(t, 1, indexRequests)
	assert.ElementsMatch(t, []string{"/api/v1/releasestream/5-stable/tags", "/api/v1/releasestream/4.23.0-0.nightly/tags", "/api/v1/releasestream/5.0.0-0.nightly/tags"}, tagRequests)
	assert.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(readChannel(t, store, "stable").Status.Conditions, privatev1.ChannelDefaultVersionAvailable).Status)
}
