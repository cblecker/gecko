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

	"github.com/openshift-online/gecko/controllers/util/logger"
	"github.com/openshift-online/gecko/controllers/versionresolution"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// mockStatusWriter discards status; status persistence is covered by ci_sync_test.go.
type mockStatusWriter struct{}

func (m *mockStatusWriter) Update(_ context.Context, _ client.Object, _ ...client.SubResourceUpdateOption) error {
	return nil
}
func (m *mockStatusWriter) Create(_ context.Context, _ client.Object, _ client.Object, _ ...client.SubResourceCreateOption) error {
	return nil
}
func (m *mockStatusWriter) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	return nil
}
func (m *mockStatusWriter) Apply(_ context.Context, _ runtime.ApplyConfiguration, _ ...client.SubResourceApplyOption) error {
	return nil
}

// mockStoreClient is a minimal client.Client backed by Version and Channel resources.
type mockStoreClient struct {
	versions       []privatev1.Version
	channels       []privatev1.Channel
	created        []*privatev1.Version
	updated        []*privatev1.Version
	deleted        []*privatev1.Version
	listedChannels bool
	listedVersions bool
}

func (m *mockStoreClient) Get(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return nil
}

func (m *mockStoreClient) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	switch typedList := list.(type) {
	case *privatev1.VersionList:
		m.listedVersions = true
		typedList.Items = append([]privatev1.Version(nil), m.versions...)
	case *privatev1.ChannelList:
		m.listedChannels = true
		typedList.Items = append([]privatev1.Channel(nil), m.channels...)
	default:
		return fmt.Errorf("unexpected list type %T", list)
	}
	return nil
}

func (m *mockStoreClient) Create(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
	version, ok := obj.(*privatev1.Version)
	if !ok {
		return fmt.Errorf("unexpected create type %T", obj)
	}
	m.created = append(m.created, version.DeepCopy())
	return nil
}

func (m *mockStoreClient) Delete(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
	version, ok := obj.(*privatev1.Version)
	if !ok {
		return fmt.Errorf("unexpected delete type %T", obj)
	}
	m.deleted = append(m.deleted, version.DeepCopy())
	return nil
}

func (m *mockStoreClient) Update(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
	version, ok := obj.(*privatev1.Version)
	if !ok {
		return fmt.Errorf("unexpected update type %T", obj)
	}
	m.updated = append(m.updated, version.DeepCopy())
	return nil
}

func (m *mockStoreClient) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
	return nil
}
func (m *mockStoreClient) DeleteAllOf(_ context.Context, _ client.Object, _ ...client.DeleteAllOfOption) error {
	return nil
}
func (m *mockStoreClient) Apply(_ context.Context, _ runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
	return nil
}
func (m *mockStoreClient) Status() client.SubResourceWriter { return &mockStatusWriter{} }
func (m *mockStoreClient) SubResource(_ string) client.SubResourceClient {
	return nil
}
func (m *mockStoreClient) Scheme() *runtime.Scheme     { return nil }
func (m *mockStoreClient) RESTMapper() meta.RESTMapper { return nil }
func (m *mockStoreClient) GroupVersionKindFor(_ runtime.Object) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, nil
}
func (m *mockStoreClient) IsObjectNamespaced(_ runtime.Object) (bool, error) {
	return false, nil
}

func newTestLogger(t *testing.T) logger.Logger {
	t.Helper()
	log, err := logger.NewLogger(logger.Config{
		Level:     "error",
		Format:    logger.FormatText,
		Component: "test",
		Version:   "test",
	})
	require.NoError(t, err)
	return log
}

func newCincinnatiServer(
	t *testing.T,
	response func(channel string) ([]versionresolution.ReleaseInfo, int),
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		releases, statusCode := response(r.URL.Query().Get("channel"))
		if statusCode != http.StatusOK {
			w.WriteHeader(statusCode)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(versionresolution.CincinnatiGraph{Nodes: releases})
	}))
}

func newController(t *testing.T, server *httptest.Server, store client.Client) *Controller {
	t.Helper()
	return NewController(
		versionresolution.NewCincinnatiClient(server.URL, "amd64"),
		newTestLogger(t),
		store,
	)
}

func TestFetchVersions(t *testing.T) {
	server := newCincinnatiServer(t, func(channel string) ([]versionresolution.ReleaseInfo, int) {
		switch channel {
		case "stable-4.22":
			return []versionresolution.ReleaseInfo{
				{Version: "4.21.9", Payload: "quay.io/release:4.21.9"},
				{Version: "4.22.11", Payload: "quay.io/release:4.22.11"},
			}, http.StatusOK
		case "fast-4.22":
			return []versionresolution.ReleaseInfo{
				{Version: "4.22.11", Payload: "quay.io/release:4.22.11"},
				{Version: "4.22.12", Payload: "quay.io/release:4.22.12"},
			}, http.StatusOK
		default:
			return nil, http.StatusOK
		}
	})
	defer server.Close()

	controller := newController(t, server, &mockStoreClient{})
	versions, err := controller.fetchVersions(context.Background(), newTestLogger(t), []privatev1.Channel{testChannel("stable", "4.22"), testChannel("fast", "4.22")})

	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, privatev1.VersionSpec{
		ChannelGroups: []string{"fast", "stable"},
		ReleaseImage:  "quay.io/release:4.22.11",
	}, versions["4.22.11"])
	assert.Equal(t, privatev1.VersionSpec{
		ChannelGroups: []string{"fast"},
		ReleaseImage:  "quay.io/release:4.22.12",
	}, versions["4.22.12"])
	assert.NotContains(t, versions, "4.21.9")
}

func TestFetchVersionsRejectsConflictingPayloads(t *testing.T) {
	server := newCincinnatiServer(t, func(channel string) ([]versionresolution.ReleaseInfo, int) {
		switch channel {
		case "stable-4.22":
			return []versionresolution.ReleaseInfo{
				{Version: "4.22.11", Payload: "quay.io/release:first"},
			}, http.StatusOK
		case "fast-4.22":
			return []versionresolution.ReleaseInfo{
				{Version: "4.22.11", Payload: "quay.io/release:second"},
			}, http.StatusOK
		default:
			return nil, http.StatusOK
		}
	})
	defer server.Close()

	controller := newController(t, server, &mockStoreClient{})
	_, err := controller.fetchVersions(context.Background(), newTestLogger(t), []privatev1.Channel{testChannel("stable", "4.22"), testChannel("fast", "4.22")})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicting release payloads")
}

func TestFetchVersionsRejectsEmptyPayload(t *testing.T) {
	server := newCincinnatiServer(t, func(channel string) ([]versionresolution.ReleaseInfo, int) {
		if channel == "stable-4.22" {
			return []versionresolution.ReleaseInfo{
				{Version: "4.22.11"},
			}, http.StatusOK
		}
		return nil, http.StatusOK
	})
	defer server.Close()

	controller := newController(t, server, &mockStoreClient{})
	_, err := controller.fetchVersions(context.Background(), newTestLogger(t), []privatev1.Channel{testChannel("stable", "4.22")})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no payload")
}

func TestSyncPreservesSnapshotOnFetchFailure(t *testing.T) {
	server := newCincinnatiServer(t, func(_ string) ([]versionresolution.ReleaseInfo, int) {
		return nil, http.StatusInternalServerError
	})
	defer server.Close()

	store := &mockStoreClient{
		channels: []privatev1.Channel{{ObjectMeta: objectMeta("stable")}},
		versions: []privatev1.Version{
			{ObjectMeta: objectMeta("4.22.11")},
		},
	}
	controller := newController(t, server, store)

	controller.sync(context.Background(), newTestLogger(t))

	assert.True(t, store.listedChannels)
	assert.False(t, store.listedVersions)
	assert.Empty(t, store.created)
	assert.Empty(t, store.updated)
	assert.Empty(t, store.deleted)
}

func TestSyncPreservesSnapshotWhenNoChannelsExist(t *testing.T) {
	server := newCincinnatiServer(t, func(_ string) ([]versionresolution.ReleaseInfo, int) {
		return []versionresolution.ReleaseInfo{
			{Version: "4.22.11", Payload: "quay.io/release:4.22.11"},
		}, http.StatusOK
	})
	defer server.Close()

	store := &mockStoreClient{}
	controller := newController(t, server, store)

	controller.sync(context.Background(), newTestLogger(t))

	assert.True(t, store.listedChannels)
	assert.False(t, store.listedVersions)
	assert.Empty(t, store.created)
	assert.Empty(t, store.updated)
	assert.Empty(t, store.deleted)
}

func TestChannelsAreReadFromChannelResources(t *testing.T) {
	store := &mockStoreClient{channels: []privatev1.Channel{
		testChannel("stable", "4.24"),
		testChannel("nightly", "4.22"),
		testChannel("prerelease", "4.23"),
	}}
	controller := &Controller{apiClient: store}

	channels, err := controller.channels(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []privatev1.Channel{store.channels[1], store.channels[2], store.channels[0]}, channels)

	assert.True(t, store.listedChannels)
}

func testChannel(name, minimum string) privatev1.Channel {
	return privatev1.Channel{
		ObjectMeta: objectMeta(name),
		Spec: privatev1.ChannelSpec{
			MinimumSupportedVersion: minimum,
			InstallDefaultVersion:   "4.22.11",
			FleetMinorVersion:       "4.22",
		},
	}
}

func TestFetchVersionsUsesMinimumSupportedVersion(t *testing.T) {
	for _, tc := range []struct {
		minimum string
		first   string
		want    []string
	}{
		{"4.21", "stable-4.21", []string{"4.21.1", "4.22.11", "4.23.1", "4.24.0-rc.1"}},
		{"4.23", "stable-4.23", []string{"4.23.1", "4.24.0-rc.1"}},
		{"4.24", "stable-4.24", []string{"4.24.0-rc.1"}},
	} {
		t.Run(tc.minimum, func(t *testing.T) {
			var queries []string
			server := newCincinnatiServer(t, func(channel string) ([]versionresolution.ReleaseInfo, int) {
				queries = append(queries, channel)
				versions := map[string]string{
					"stable-4.21": "4.21.1", "stable-4.22": "4.22.11",
					"stable-4.23": "4.23.1", "stable-4.24": "4.24.0-rc.1",
				}
				if version, ok := versions[channel]; ok {
					return []versionresolution.ReleaseInfo{
						{Version: version, Payload: "image:" + version},
						{Version: "4.20.1", Payload: "old-upgrade-source"},
						{Version: "v4.24.0", Payload: "non-canonical"},
					}, http.StatusOK
				}
				return nil, http.StatusOK
			})
			defer server.Close()
			controller := newController(t, server, &mockStoreClient{})
			versions, err := controller.fetchVersions(context.Background(), newTestLogger(t), []privatev1.Channel{testChannel("stable", tc.minimum)})
			require.NoError(t, err)
			require.NotEmpty(t, queries)
			assert.Equal(t, tc.first, queries[0])
			assert.Contains(t, queries, "stable-4.24")
			var names []string
			for name := range versions {
				names = append(names, name)
			}
			assert.ElementsMatch(t, tc.want, names)
		})
	}
}

func TestFetchVersionsUsesIndependentChannelMinima(t *testing.T) {
	server := newCincinnatiServer(t, func(channel string) ([]versionresolution.ReleaseInfo, int) {
		switch channel {
		case "stable-4.22", "stable-4.24", "fast-4.24":
			return []versionresolution.ReleaseInfo{
				{Version: "4.22.11", Payload: "old"},
				{Version: "4.24.0", Payload: "new"},
			}, http.StatusOK
		default:
			return nil, http.StatusOK
		}
	})
	defer server.Close()
	controller := newController(t, server, &mockStoreClient{})
	versions, err := controller.fetchVersions(context.Background(), newTestLogger(t), []privatev1.Channel{testChannel("stable", "4.22"), testChannel("fast", "4.24")})
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, []string{"stable"}, versions["4.22.11"].ChannelGroups)
	assert.Equal(t, []string{"fast", "stable"}, versions["4.24.0"].ChannelGroups)
}

func TestInvalidMinimumSupportedVersionPreservesSnapshot(t *testing.T) {
	for _, minimum := range []string{"", "4", "4.22.1", "-1.0", "4.-1", "04.22", "4.022", "4.22x"} {
		t.Run(minimum, func(t *testing.T) {
			server := newCincinnatiServer(t, func(string) ([]versionresolution.ReleaseInfo, int) {
				t.Error("invalid minimum should fail before requesting Cincinnati")
				return nil, http.StatusOK
			})
			defer server.Close()
			store := &mockStoreClient{
				channels: []privatev1.Channel{testChannel("stable", minimum)},
				versions: []privatev1.Version{{ObjectMeta: objectMeta("4.22.11")}},
			}
			newController(t, server, store).sync(context.Background(), newTestLogger(t))
			assert.False(t, store.listedVersions)
			assert.Empty(t, store.created)
			assert.Empty(t, store.updated)
			assert.Empty(t, store.deleted)
		})
	}
}

func TestAdvancingFleetMinorPreservesSupportedVersionsAndInstallDefault(t *testing.T) {
	server := newCincinnatiServer(t, func(channel string) ([]versionresolution.ReleaseInfo, int) {
		switch channel {
		case "stable-4.22":
			return []versionresolution.ReleaseInfo{{Version: "4.22.11", Payload: "old"}}, http.StatusOK
		case "stable-4.23":
			return []versionresolution.ReleaseInfo{{Version: "4.23.1", Payload: "new"}}, http.StatusOK
		default:
			return nil, http.StatusOK
		}
	})
	defer server.Close()
	store := &mockStoreClient{channels: []privatev1.Channel{testChannel("stable", "4.22")}}
	controller := newController(t, server, store)
	controller.sync(context.Background(), newTestLogger(t))
	require.Len(t, store.created, 2)
	for _, version := range store.created {
		store.versions = append(store.versions, *version.DeepCopy())
	}
	store.created = nil
	store.channels[0].Spec.FleetMinorVersion = "4.23"
	controller.sync(context.Background(), newTestLogger(t))
	controller.sync(context.Background(), newTestLogger(t))
	assert.Empty(t, store.created)
	assert.Empty(t, store.updated)
	assert.Empty(t, store.deleted)
	assert.Equal(t, "4.22.11", store.channels[0].Spec.InstallDefaultVersion)
}

func TestRaisingMinimumSupportedVersionRemovesOlderMembership(t *testing.T) {
	server := newCincinnatiServer(t, func(channel string) ([]versionresolution.ReleaseInfo, int) {
		switch channel {
		case "stable-4.24", "fast-4.22", "fast-4.24":
			return []versionresolution.ReleaseInfo{{Version: "4.22.11", Payload: "old"}, {Version: "4.24.0", Payload: "new"}}, http.StatusOK
		default:
			return nil, http.StatusOK
		}
	})
	defer server.Close()
	store := &mockStoreClient{
		channels: []privatev1.Channel{testChannel("stable", "4.24"), testChannel("fast", "4.22")},
		versions: []privatev1.Version{
			{ObjectMeta: objectMeta("4.22.11"), Spec: privatev1.VersionSpec{ReleaseImage: "old", ChannelGroups: []string{"fast", "stable"}}},
			{ObjectMeta: objectMeta("4.22.12"), Spec: privatev1.VersionSpec{ReleaseImage: "stale", ChannelGroups: []string{"stable"}}},
		},
	}
	newController(t, server, store).sync(context.Background(), newTestLogger(t))
	require.Len(t, store.updated, 1)
	assert.Equal(t, "4.22.11", store.updated[0].Name)
	assert.Equal(t, []string{"fast"}, store.updated[0].Spec.ChannelGroups)
	require.Len(t, store.deleted, 1)
	assert.Equal(t, "4.22.12", store.deleted[0].Name)
	require.Len(t, store.created, 1)
	assert.Equal(t, "4.24.0", store.created[0].Name)
	assert.Equal(t, []string{"fast", "stable"}, store.created[0].Spec.ChannelGroups)
}

func TestApplyCreatesUpdatesAndDeletesVersions(t *testing.T) {
	store := &mockStoreClient{versions: []privatev1.Version{
		{
			ObjectMeta: objectMeta("4.22.9"),
			Spec: privatev1.VersionSpec{
				ChannelGroups: []string{"stable"},
				ReleaseImage:  "quay.io/release:4.22.9",
			},
		},
		{
			ObjectMeta: objectMeta("4.22.10"),
			Spec: privatev1.VersionSpec{
				ChannelGroups: []string{"stable"},
				ReleaseImage:  "quay.io/release:old",
			},
		},
		{
			ObjectMeta: objectMeta("4.22.11"),
			Spec: privatev1.VersionSpec{
				ChannelGroups: []string{"stable"},
				ReleaseImage:  "quay.io/release:4.22.11",
			},
		},
	}}
	controller := &Controller{apiClient: store}
	desired := map[string]privatev1.VersionSpec{
		"4.22.10": {
			ChannelGroups: []string{"fast", "stable"},
			ReleaseImage:  "quay.io/release:4.22.10",
		},
		"4.22.11": {
			ChannelGroups: []string{"stable"},
			ReleaseImage:  "quay.io/release:4.22.11",
		},
		"4.22.12": {
			ChannelGroups: []string{"fast"},
			ReleaseImage:  "quay.io/release:4.22.12",
		},
	}

	err := controller.apply(context.Background(), newTestLogger(t), desired)

	require.NoError(t, err)
	require.Len(t, store.updated, 1)
	assert.Equal(t, "4.22.10", store.updated[0].Name)
	assert.Equal(t, desired["4.22.10"], store.updated[0].Spec)
	require.Len(t, store.created, 1)
	assert.Equal(t, "4.22.12", store.created[0].Name)
	assert.Equal(t, desired["4.22.12"], store.created[0].Spec)
	require.Len(t, store.deleted, 1)
	assert.Equal(t, "4.22.9", store.deleted[0].Name)
}

func objectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name}
}
