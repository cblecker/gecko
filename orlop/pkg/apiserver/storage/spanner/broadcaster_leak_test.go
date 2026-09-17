package spanner

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
)

// TestHandleChildPartitionsRecord_SkipsDuplicateTokens verifies that
// handleChildPartitionsRecord does not spawn a second goroutine for a
// token that was already spawned. Before the fix, pendingChildren deleted
// the token after spawning, so a re-delivered ChildPartitionsRecord would
// create a duplicate goroutine.
func TestHandleChildPartitionsRecord_SkipsDuplicateTokens(t *testing.T) {
	// Cancel context immediately so any spawned goroutines exit before
	// reaching b.client.Single() (client is nil in this unit test).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	b := &spannerBroadcaster{
		ctx:             ctx,
		cancel:          cancel,
		subscribers:     make(map[int]chan storage.ResourceEvent),
		pendingChildren: make(map[string]int),
		spawnedChildren: make(map[string]struct{}),
	}

	rec := &csChildPartitionsRecord{
		StartTimestamp: time.Now(),
		ChildPartitions: []*csChildPartition{
			{Token: "token-A", ParentPartitionTokens: []string{"root"}},
			{Token: "token-B", ParentPartitionTokens: []string{"root"}},
		},
	}

	// First call — should spawn goroutines for both tokens.
	b.handleChildPartitionsRecord(ctx, rec)

	if _, ok := b.spawnedChildren["token-A"]; !ok {
		t.Fatal("token-A should be in spawnedChildren after first call")
	}
	if _, ok := b.spawnedChildren["token-B"]; !ok {
		t.Fatal("token-B should be in spawnedChildren after first call")
	}

	// Goroutines exit immediately (context already canceled).
	b.wg.Wait()

	// Second call with same tokens — should NOT spawn new goroutines.
	goroutinesBefore := runtime.NumGoroutine()
	b.handleChildPartitionsRecord(ctx, rec)
	goroutinesAfter := runtime.NumGoroutine()

	if len(b.pendingChildren) != 0 {
		t.Errorf("pendingChildren should be empty, got %d entries", len(b.pendingChildren))
	}

	if goroutinesAfter > goroutinesBefore+1 {
		t.Errorf("goroutine leak: before=%d after=%d (expected no growth)", goroutinesBefore, goroutinesAfter)
	}
}

// TestHandleChildPartitionsRecord_MergeWaitsForAllParents verifies that
// when multiple parents report the same child token (partition merge),
// the child goroutine starts only after all parents have reported it.
func TestHandleChildPartitionsRecord_MergeWaitsForAllParents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so spawned goroutines exit right away

	b := &spannerBroadcaster{
		ctx:             ctx,
		cancel:          cancel,
		subscribers:     make(map[int]chan storage.ResourceEvent),
		pendingChildren: make(map[string]int),
		spawnedChildren: make(map[string]struct{}),
	}

	ts := time.Now()

	// Child token "merged" has two parents.
	rec1 := &csChildPartitionsRecord{
		StartTimestamp: ts,
		ChildPartitions: []*csChildPartition{
			{Token: "merged", ParentPartitionTokens: []string{"parent-1", "parent-2"}},
		},
	}

	// First parent reports — counter should be 1, not yet spawned.
	b.handleChildPartitionsRecord(ctx, rec1)

	if _, ok := b.spawnedChildren["merged"]; ok {
		t.Fatal("merged token should not be spawned after only first parent reports")
	}
	if count, ok := b.pendingChildren["merged"]; !ok || count != 1 {
		t.Fatalf("expected pendingChildren[merged]=1, got %d (exists=%v)", count, ok)
	}

	// Second parent reports — counter reaches 0, should spawn.
	b.handleChildPartitionsRecord(ctx, rec1)

	if _, ok := b.spawnedChildren["merged"]; !ok {
		t.Fatal("merged token should be spawned after both parents report")
	}
	if _, ok := b.pendingChildren["merged"]; ok {
		t.Fatal("merged should be removed from pendingChildren after spawn")
	}

	b.wg.Wait()
}

// TestBroadcaster_NoGoroutineLeak verifies that the broadcaster's
// change-stream partition readers reach a stable goroutine count and
// do not grow unboundedly over time. This is the integration-level test
// for the root-cause fix: readChangeStream must exit after processing
// a ChildPartitionsRecord instead of looping and re-querying.
func TestBroadcaster_NoGoroutineLeak(t *testing.T) {
	broadcaster, _ := setupBroadcasterWithStore(t)

	// Give the broadcaster time to start partition readers and settle.
	time.Sleep(3 * time.Second)
	baseline := runtime.NumGoroutine()

	// Wait another 5 seconds. If the old bug existed, the root partition
	// reader would spawn ~10 new goroutines/second.
	time.Sleep(5 * time.Second)
	after := runtime.NumGoroutine()

	// Allow small variance (GC, timers, other tests) but catch leaks.
	growth := after - baseline
	if growth > 5 {
		t.Errorf("goroutine leak detected: baseline=%d, after 5s=%d, growth=%d",
			baseline, after, growth)
	}

	// Verify Close() actually stops everything.
	if err := broadcaster.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	// Give goroutines time to wind down.
	time.Sleep(time.Second)
	afterClose := runtime.NumGoroutine()

	if afterClose > baseline {
		t.Errorf("goroutines not cleaned up after Close(): baseline=%d, afterClose=%d",
			baseline, afterClose)
	}
}

// TestSpannerStore_Close verifies that calling Close() on a SpannerStore
// shuts down the broadcaster and its partition reader goroutines.
func TestSpannerStore_Close(t *testing.T) {
	broadcaster, store := setupBroadcasterWithStore(t)

	// Subscribe to verify the broadcaster is alive.
	ch, stop, err := broadcaster.Subscribe("")
	if err != nil {
		t.Fatalf("Subscribe() failed before Close: %v", err)
	}
	stop()

	// Close via the store.
	if err := store.Close(); err != nil {
		t.Fatalf("SpannerStore.Close() failed: %v", err)
	}

	// Broadcaster should reject new subscriptions.
	_, _, err = broadcaster.Subscribe("")
	if err == nil {
		t.Error("expected error subscribing to closed broadcaster")
	}

	// The subscriber channel should be closed.
	select {
	case _, ok := <-ch:
		if ok {
			// One buffered event is fine — drain it.
			select {
			case _, ok := <-ch:
				if ok {
					t.Error("channel still open after Close")
				}
			case <-time.After(2 * time.Second):
				t.Error("channel not closed after Close")
			}
		}
	case <-time.After(2 * time.Second):
		t.Error("channel not closed after Close")
	}
}
