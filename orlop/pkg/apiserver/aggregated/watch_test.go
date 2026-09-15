package aggregated

import (
	"testing"
	"time"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

func newTestClientObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "test.example.com/v1",
			"kind":       "TestObject",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": "default",
			},
		},
	}
}

func TestWatchAdapterTranslatesEvents(t *testing.T) {
	tests := []struct {
		name          string
		storageType   storage.EventType
		expectedWatch watch.EventType
	}{
		{
			name:          "ADDED maps to watch.Added",
			storageType:   storage.EventAdded,
			expectedWatch: watch.Added,
		},
		{
			name:          "MODIFIED maps to watch.Modified",
			storageType:   storage.EventModified,
			expectedWatch: watch.Modified,
		},
		{
			name:          "DELETED maps to watch.Deleted",
			storageType:   storage.EventDeleted,
			expectedWatch: watch.Deleted,
		},
		{
			name:          "BOOKMARK maps to watch.Bookmark",
			storageType:   storage.EventBookmark,
			expectedWatch: watch.Bookmark,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan storage.ResourceEvent, 1)
			stopCalled := false
			w := newWatchAdapter(ch, func() { stopCalled = true })

			obj := newTestClientObject("obj1")
			ch <- storage.ResourceEvent{
				Type:   tc.storageType,
				Object: obj,
			}

			select {
			case ev := <-w.ResultChan():
				if ev.Type != tc.expectedWatch {
					t.Errorf("expected event type %s, got %s", tc.expectedWatch, ev.Type)
				}
				if ev.Object == nil {
					t.Error("expected non-nil object in event")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for event")
			}

			w.Stop()
			if !stopCalled {
				t.Error("expected stopFn to be called")
			}
		})
	}
}

func TestWatchAdapterStop(t *testing.T) {
	ch := make(chan storage.ResourceEvent)
	stopCalled := false
	w := newWatchAdapter(ch, func() { stopCalled = true })

	w.Stop()

	if !stopCalled {
		t.Error("expected stopFn to be called on Stop()")
	}

	// Close the input channel so the goroutine terminates and result channel closes.
	close(ch)

	select {
	case <-w.ResultChan():
		// Drain any residual; the channel should eventually close.
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for result channel to close after Stop()")
	}
}

func TestWatchAdapterCloseOnChannelClose(t *testing.T) {
	ch := make(chan storage.ResourceEvent)
	w := newWatchAdapter(ch, func() {})

	close(ch)

	// ResultChan should eventually be closed.
	select {
	case _, ok := <-w.ResultChan():
		if ok {
			t.Error("expected result channel to be closed, but received an event")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for result channel to close")
	}
}

func TestWatchAdapterStopMultipleTimes(t *testing.T) {
	ch := make(chan storage.ResourceEvent)
	w := newWatchAdapter(ch, func() {})

	// Calling Stop multiple times must not panic.
	w.Stop()
	w.Stop()
	w.Stop()

	close(ch)
}

func TestWatchAdapterGoroutineExitsOnDone(t *testing.T) {
	ch := make(chan storage.ResourceEvent, 200)
	w := newWatchAdapter(ch, func() {})

	obj := newTestClientObject("fill-obj")

	// Fill the result channel buffer (capacity 100) plus extra events
	// that the goroutine cannot deliver because the buffer is full.
	for i := 0; i < 150; i++ {
		ch <- storage.ResourceEvent{
			Type:   storage.EventAdded,
			Object: obj,
		}
	}

	// Give the goroutine a moment to fill the buffer and block on the 101st send.
	time.Sleep(50 * time.Millisecond)

	// Stop should unblock the goroutine via the done channel.
	w.Stop()
	close(ch)

	// The result channel must eventually close, proving the goroutine exited.
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-w.ResultChan():
			if !ok {
				return // success: channel closed, goroutine exited
			}
		case <-timeout:
			t.Fatal("timed out waiting for result channel to close; goroutine likely leaked")
		}
	}
}

func TestInitialEventsWatch(t *testing.T) {
	obj1 := newTestClientObject("obj1")
	obj2 := newTestClientObject("obj2")

	bookmark := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "test.example.com/v1",
			"kind":       "TestObject",
			"metadata": map[string]interface{}{
				"resourceVersion": "42",
				"annotations": map[string]interface{}{
					metav1.InitialEventsAnnotationKey: "true",
				},
			},
		},
	}

	eventCh := make(chan storage.ResourceEvent, 10)
	stopCalled := false
	w := newInitialEventsWatch(
		[]runtime.Object{obj1, obj2},
		bookmark,
		eventCh,
		func() { stopCalled = true },
	)

	recv := func() watch.Event {
		t.Helper()
		select {
		case ev := <-w.ResultChan():
			return ev
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for event")
			return watch.Event{}
		}
	}

	ev1 := recv()
	if ev1.Type != watch.Added {
		t.Errorf("expected first event Added, got %s", ev1.Type)
	}

	ev2 := recv()
	if ev2.Type != watch.Added {
		t.Errorf("expected second event Added, got %s", ev2.Type)
	}

	ev3 := recv()
	if ev3.Type != watch.Bookmark {
		t.Errorf("expected bookmark event, got %s", ev3.Type)
	}
	bmObj := ev3.Object.(*unstructured.Unstructured)
	annotations := bmObj.GetAnnotations()
	if annotations[metav1.InitialEventsAnnotationKey] != "true" {
		t.Errorf("expected initial-events-end annotation on bookmark, got %v", annotations)
	}

	// Now send a real event and verify it flows through
	eventCh <- storage.ResourceEvent{
		Type:   storage.EventModified,
		Object: newTestClientObject("obj1-modified"),
	}

	ev4 := recv()
	if ev4.Type != watch.Modified {
		t.Errorf("expected forwarded Modified event, got %s", ev4.Type)
	}

	w.Stop()
	if !stopCalled {
		t.Error("expected stopFn to be called")
	}
}

func TestInitialEventsWatchEmpty(t *testing.T) {
	bookmark := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "test.example.com/v1",
			"kind":       "TestObject",
			"metadata": map[string]interface{}{
				"resourceVersion": "0",
				"annotations": map[string]interface{}{
					metav1.InitialEventsAnnotationKey: "true",
				},
			},
		},
	}

	eventCh := make(chan storage.ResourceEvent, 10)
	w := newInitialEventsWatch(
		nil,
		bookmark,
		eventCh,
		func() {},
	)

	// With no items, first event should be the bookmark
	select {
	case ev := <-w.ResultChan():
		if ev.Type != watch.Bookmark {
			t.Errorf("expected bookmark as first event when items empty, got %s", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bookmark")
	}

	w.Stop()
	close(eventCh)
}

func TestInitialEventsWatchStopDuringInitial(t *testing.T) {
	items := make([]runtime.Object, 200)
	for i := range items {
		items[i] = newTestClientObject("obj")
	}

	bookmark := newTestClientObject("bookmark")
	eventCh := make(chan storage.ResourceEvent)
	w := newInitialEventsWatch(items, bookmark, eventCh, func() {})

	// Drain a few, then stop — goroutine should exit cleanly
	for i := 0; i < 5; i++ {
		<-w.ResultChan()
	}

	w.Stop()
	close(eventCh)

	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-w.ResultChan():
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for result channel to close")
		}
	}
}

func TestWatchAdapterUnknownEventType(t *testing.T) {
	ch := make(chan storage.ResourceEvent, 2)
	w := newWatchAdapter(ch, func() {})

	obj := newTestClientObject("unknown-obj")
	// Send an unknown event type followed by a known one.
	ch <- storage.ResourceEvent{
		Type:   storage.EventType("UNKNOWN"),
		Object: obj,
	}
	ch <- storage.ResourceEvent{
		Type:   storage.EventAdded,
		Object: obj,
	}

	select {
	case ev := <-w.ResultChan():
		// The unknown event should be skipped; we should receive the ADDED event.
		if ev.Type != watch.Added {
			t.Errorf("expected first received event to be Added (unknown skipped), got %s", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event after unknown type was sent")
	}
}
