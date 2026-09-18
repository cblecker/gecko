package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// errWriter is an http.ResponseWriter that returns an error on Write
// after a configurable number of successful writes.
type errWriter struct {
	header       http.Header
	flushed      int32
	writeCount   int32
	failAfter    int32 // fail on writes after this many successful ones
	statusCode   int
	writtenBytes []byte
}

func newErrWriter(failAfter int32) *errWriter {
	return &errWriter{
		header:    make(http.Header),
		failAfter: failAfter,
	}
}

func (w *errWriter) Header() http.Header { return w.header }

func (w *errWriter) WriteHeader(code int) { w.statusCode = code }

func (w *errWriter) Write(b []byte) (int, error) {
	n := atomic.AddInt32(&w.writeCount, 1)
	if n > w.failAfter {
		return 0, errors.New("simulated write error")
	}
	w.writtenBytes = append(w.writtenBytes, b...)
	return len(b), nil
}

func (w *errWriter) Flush() { atomic.AddInt32(&w.flushed, 1) }

// fakeStore implements storage.ResourceStore for testing.
type fakeStore struct{}

func (s *fakeStore) Create(_ context.Context, _ client.Object) error           { return nil }
func (s *fakeStore) Get(_ context.Context, _, _ string) (client.Object, error) { return nil, nil }
func (s *fakeStore) Update(_ context.Context, _ client.Object) error           { return nil }
func (s *fakeStore) Delete(_ context.Context, _, _ string) error               { return nil }

func (s *fakeStore) List(_ context.Context, _ storage.ListOptions) (client.ObjectList, error) {
	return &unstructured.UnstructuredList{}, nil
}

func (s *fakeStore) Watch(_ context.Context, _ storage.ListOptions, _ string) (<-chan storage.ResourceEvent, func(), error) {
	return nil, nil, nil
}

// TestStreamWatch_InitialBookmarkWriteError verifies that streamWatch returns
// immediately when the initial-events-end bookmark write fails.
func TestStreamWatch_InitialBookmarkWriteError(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Widget"}

	// Allow 0 writes - the bookmark Encode will fail immediately.
	w := newErrWriter(0)
	streamer := &watchStreamer{
		writer:                 w,
		flusher:                w,
		encoder:                json.NewEncoder(w),
		gvk:                    gvk,
		lastResourceVersion:    "5",
		currentResourceVersion: "5",
	}

	eventCh := make(chan storage.ResourceEvent)
	defer close(eventCh)

	config := watchConfig{
		allowWatchBookmarks: true,
		sendInitialEvents:   true, // triggers the initial-events-end bookmark path
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		streamWatch(context.Background(), streamer, eventCh, config, storage.ListOptions{}, &fakeStore{}, nil)
	}()

	select {
	case <-done:
		// streamWatch returned — correct behavior on bookmark write failure.
	case <-time.After(2 * time.Second):
		t.Fatal("streamWatch did not return after initial bookmark write failure")
	}
}

// TestStreamWatch_CaughtUpBookmarkWriteError verifies that streamWatch returns
// immediately when the sendInitialBookmarkIfCaughtUp call fails.
func TestStreamWatch_CaughtUpBookmarkWriteError(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Widget"}

	// Allow 0 writes so the caught-up bookmark Encode fails immediately.
	w := newErrWriter(0)
	streamer := &watchStreamer{
		writer:                 w,
		flusher:                w,
		encoder:                json.NewEncoder(w),
		gvk:                    gvk,
		lastResourceVersion:    "5",
		currentResourceVersion: "5",
		listIsEmpty:            true, // ensures sendInitialBookmarkIfCaughtUp sends a bookmark
	}

	eventCh := make(chan storage.ResourceEvent)
	defer close(eventCh)

	config := watchConfig{
		allowWatchBookmarks: true,
		sendInitialEvents:   false, // skip initial events; triggers the caught-up bookmark path
		resourceVersion:     "5",   // matches currentRV so bookmark fires
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		streamWatch(context.Background(), streamer, eventCh, config, storage.ListOptions{}, &fakeStore{}, nil)
	}()

	select {
	case <-done:
		// streamWatch returned — correct behavior on caught-up bookmark write failure.
	case <-time.After(2 * time.Second):
		t.Fatal("streamWatch did not return after caught-up bookmark write failure")
	}
}
