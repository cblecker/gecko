package spanner

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/go-logr/logr"
	"google.golang.org/api/iterator"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type csColumnType struct {
	Name            string           `spanner:"name"`
	Type            spanner.NullJSON `spanner:"type"`
	IsPrimaryKey    bool             `spanner:"is_primary_key"`
	OrdinalPosition int64            `spanner:"ordinal_position"`
}

type csMod struct {
	Keys      spanner.NullJSON `spanner:"keys"`
	NewValues spanner.NullJSON `spanner:"new_values"`
	OldValues spanner.NullJSON `spanner:"old_values"`
}

type csDataChangeRecord struct {
	CommitTimestamp                      time.Time       `spanner:"commit_timestamp"`
	RecordSequence                       string          `spanner:"record_sequence"`
	ServerTransactionID                  string          `spanner:"server_transaction_id"`
	IsLastRecordInTransactionInPartition bool            `spanner:"is_last_record_in_transaction_in_partition"`
	TableName                            string          `spanner:"table_name"`
	ColumnTypes                          []*csColumnType `spanner:"column_types"`
	Mods                                 []*csMod        `spanner:"mods"`
	ModType                              string          `spanner:"mod_type"`
	ValueCaptureType                     string          `spanner:"value_capture_type"`
	NumberOfRecordsInTransaction         int64           `spanner:"number_of_records_in_transaction"`
	NumberOfPartitionsInTransaction      int64           `spanner:"number_of_partitions_in_transaction"`
	TransactionTag                       string          `spanner:"transaction_tag"`
	IsSystemTransaction                  bool            `spanner:"is_system_transaction"`
}

type csHeartbeatRecord struct {
	Timestamp time.Time `spanner:"timestamp"`
}

type csChildPartition struct {
	Token                 string   `spanner:"token"`
	ParentPartitionTokens []string `spanner:"parent_partition_tokens"`
}

type csChildPartitionsRecord struct {
	StartTimestamp  time.Time           `spanner:"start_timestamp"`
	RecordSequence  string              `spanner:"record_sequence"`
	ChildPartitions []*csChildPartition `spanner:"child_partitions"`
}

type csRecord struct {
	DataChangeRecords      []*csDataChangeRecord      `spanner:"data_change_record"`
	HeartbeatRecords       []*csHeartbeatRecord       `spanner:"heartbeat_record"`
	ChildPartitionsRecords []*csChildPartitionsRecord `spanner:"child_partitions_record"`
}

type spannerBroadcaster struct {
	client                *spanner.Client
	resourceType          string
	tableName             string
	changeStreamName      string
	heartbeatMilliseconds int64
	ctx                   context.Context
	cancel                context.CancelFunc
	scheme                *runtime.Scheme
	gvk                   schema.GroupVersionKind
	logger                logr.Logger

	mu          sync.RWMutex
	subscribers map[int]chan storage.ResourceEvent
	nextID      int
	closed      bool
	lastRV      int64
	wg          sync.WaitGroup

	// pendingChildren tracks child partition tokens during partition merges.
	// When multiple parents merge into one child, each parent independently
	// reports the same child token. The counter tracks how many parents have
	// yet to finish; the child reader starts only when all parents are done.
	pendingChildren map[string]int

	// spawnedChildren records every child token for which a reader goroutine
	// has already been launched. This prevents duplicate goroutines when a
	// parent partition re-queries the change stream and receives the same
	// ChildPartitionsRecord again.
	spawnedChildren map[string]struct{}
}

type spannerBroadcasterConfig struct {
	Client                *spanner.Client
	ResourceType          string
	TableName             string
	ChangeStreamName      string
	Scheme                *runtime.Scheme
	GVK                   schema.GroupVersionKind
	Logger                logr.Logger
	HeartbeatMilliseconds int64 // change-stream heartbeat interval; 0 defaults to 5000
}

func newSpannerBroadcaster(ctx context.Context, config spannerBroadcasterConfig) (*spannerBroadcaster, error) {
	if config.Client == nil {
		return nil, fmt.Errorf("spanner client is required")
	}

	tableName := config.TableName
	if tableName == "" {
		tableName = "resources"
	}

	bCtx, cancel := context.WithCancel(ctx)

	broadcasterLogger := config.Logger
	if broadcasterLogger.GetSink() == nil {
		broadcasterLogger = logr.Discard()
	}

	heartbeat := config.HeartbeatMilliseconds
	if heartbeat <= 0 {
		heartbeat = 5000
	}

	b := &spannerBroadcaster{
		client:                config.Client,
		resourceType:          config.ResourceType,
		tableName:             tableName,
		changeStreamName:      config.ChangeStreamName,
		ctx:                   bCtx,
		cancel:                cancel,
		scheme:                config.Scheme,
		gvk:                   config.GVK,
		logger:                broadcasterLogger,
		heartbeatMilliseconds: heartbeat,
		subscribers:           make(map[int]chan storage.ResourceEvent),
		pendingChildren:       make(map[string]int),
		spawnedChildren:       make(map[string]struct{}),
	}

	startTs := time.Now()
	b.wg.Go(func() {
		b.readChangeStream(bCtx, nil, startTs)
	})

	return b, nil
}

// readChangeStream reads a single change stream partition. Each partition
// tracks its own checkpoint (lastTs) so that a reconnect resumes from where
// this partition left off, not from the most-advanced partition's timestamp.
//
// When a partition yields a ChildPartitionsRecord (split/merge), child
// readers are launched by handleChildPartitionsRecord. The spawnedChildren
// set ensures each child token is started at most once, even if the same
// ChildPartitionsRecord is seen again on a subsequent query iteration.
func (b *spannerBroadcaster) readChangeStream(ctx context.Context, partitionToken *string, startTs time.Time) {
	backoff := time.Second
	lastTs := startTs
	resource := b.gvk.GroupVersion().String() + "/" + b.gvk.Kind

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		params := map[string]any{
			"startTimestamp":        lastTs,
			"endTimestamp":          (*time.Time)(nil),
			"partitionToken":        partitionToken,
			"heartbeatMilliseconds": b.heartbeatMilliseconds,
		}

		stmt := spanner.Statement{
			SQL: fmt.Sprintf(
				"SELECT ChangeRecord FROM READ_%s(start_timestamp => @startTimestamp, end_timestamp => @endTimestamp, partition_token => @partitionToken, heartbeat_milliseconds => @heartbeatMilliseconds)",
				b.changeStreamName,
			),
			Params: params,
		}

		iter := b.client.Single().Query(ctx, stmt)
		sawChildren, err := b.processChangeRecords(ctx, iter, &lastTs)
		iter.Stop()

		if ctx.Err() != nil {
			return
		}

		if err != nil {
			b.logger.Error(err, "Change stream read error, retrying",
				"resource", resource,
				"backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}

		// A ChildPartitionsRecord signals that this partition has been
		// split or merged. Child readers were started (or already
		// exist) via handleChildPartitionsRecord; this goroutine's
		// slice of the change stream has been handed off.
		if sawChildren {
			return
		}

		backoff = time.Second
	}
}

// processChangeRecords drains the iterator and dispatches each record type.
// It returns sawChildren=true when at least one ChildPartitionsRecord was
// present, signaling that this partition has been split/merged and the
// caller should exit.
func (b *spannerBroadcaster) processChangeRecords(ctx context.Context, iter *spanner.RowIterator, lastTs *time.Time) (sawChildren bool, err error) {
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			return sawChildren, nil
		}
		if err != nil {
			return sawChildren, err
		}

		var records []*csRecord
		if err := row.Column(0, &records); err != nil {
			continue
		}

		for _, rec := range records {
			for _, dcr := range rec.DataChangeRecords {
				b.handleDataChangeRecord(dcr)
				if dcr.CommitTimestamp.After(*lastTs) {
					*lastTs = dcr.CommitTimestamp
				}
			}

			for _, hr := range rec.HeartbeatRecords {
				if hr.Timestamp.After(*lastTs) {
					*lastTs = hr.Timestamp
				}
			}

			for _, cpr := range rec.ChildPartitionsRecords {
				b.handleChildPartitionsRecord(ctx, cpr)
				if cpr.StartTimestamp.After(*lastTs) {
					*lastTs = cpr.StartTimestamp
				}
				sawChildren = true
			}
		}
	}
}

func (b *spannerBroadcaster) handleDataChangeRecord(dcr *csDataChangeRecord) {
	var eventType storage.EventType
	switch dcr.ModType {
	case "INSERT":
		eventType = storage.EventAdded
	case "UPDATE":
		eventType = storage.EventModified
	case "DELETE":
		eventType = storage.EventDeleted
	default:
		return
	}

	for _, m := range dcr.Mods {
		keys, err := csModToMap(m.Keys)
		if err != nil {
			continue
		}

		resourceType, _ := keys["resource_type"].(string)
		if resourceType != b.resourceType {
			continue
		}

		contextFilter, _ := keys["context_filter"].(string)

		// For DELETE, new_values is empty — read from old_values instead
		var values map[string]any
		if dcr.ModType == "DELETE" {
			values, err = csModToMap(m.OldValues)
		} else {
			values, err = csModToMap(m.NewValues)
		}
		if err != nil {
			continue
		}

		rv, _ := jsonInt64(values["resource_version"])

		objectData := values["data"]
		if objectData == nil {
			continue
		}
		var objectDataBytes []byte
		switch v := objectData.(type) {
		case string:
			objectDataBytes = []byte(v)
		default:
			objectDataBytes, err = json.Marshal(v)
			if err != nil {
				continue
			}
		}

		obj, err := b.reconstructObject(objectDataBytes)
		if err != nil {
			continue
		}

		obj.SetResourceVersion(strconv.FormatInt(rv, 10))

		event := storage.ResourceEvent{
			Type:               eventType,
			ResourceVersion:    strconv.FormatInt(rv, 10),
			Object:             obj,
			ContextFilterValue: contextFilter,
		}

		b.broadcastToSubscribers(event)

		b.mu.Lock()
		if rv > b.lastRV {
			b.lastRV = rv
		}
		b.mu.Unlock()
	}
}

func csModToMap(nj spanner.NullJSON) (map[string]any, error) {
	if !nj.Valid {
		return nil, fmt.Errorf("null JSON")
	}
	data, err := json.Marshal(nj.Value)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func jsonInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}

func (b *spannerBroadcaster) handleChildPartitionsRecord(ctx context.Context, cpr *csChildPartitionsRecord) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, cp := range cpr.ChildPartitions {
		token := cp.Token
		startTs := cpr.StartTimestamp

		// Skip tokens for which a reader goroutine was already launched.
		if _, exists := b.spawnedChildren[token]; exists {
			continue
		}

		if _, exists := b.pendingChildren[token]; !exists {
			b.pendingChildren[token] = len(cp.ParentPartitionTokens)
		}
		b.pendingChildren[token]--

		if b.pendingChildren[token] <= 0 {
			delete(b.pendingChildren, token)
			b.spawnedChildren[token] = struct{}{}
			b.wg.Go(func() {
				b.readChangeStream(ctx, &token, startTs)
			})
		}
	}
}

func (b *spannerBroadcaster) reconstructObject(data []byte) (client.Object, error) {
	if b.scheme == nil || b.gvk.Empty() {
		obj := &unstructured.Unstructured{}
		if err := json.Unmarshal(data, &obj.Object); err != nil {
			return nil, err
		}
		return obj, nil
	}

	obj, err := b.scheme.New(b.gvk)
	if err != nil {
		unstruct := &unstructured.Unstructured{}
		if err := json.Unmarshal(data, &unstruct.Object); err != nil {
			return nil, err
		}
		unstruct.SetGroupVersionKind(b.gvk)
		return unstruct, nil
	}

	if err := json.Unmarshal(data, obj); err != nil {
		return nil, fmt.Errorf("failed to unmarshal into typed object: %w", err)
	}

	obj.GetObjectKind().SetGroupVersionKind(b.gvk)

	clientObj, ok := obj.(client.Object)
	if !ok {
		return nil, fmt.Errorf("object does not implement client.Object")
	}

	return clientObj, nil
}

func (b *spannerBroadcaster) broadcastToSubscribers(event storage.ResourceEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for id, ch := range b.subscribers {
		select {
		case ch <- event:
		default:
			// Channel full — cancel this watch so the controller knows to relist
			close(ch)
			delete(b.subscribers, id)
		}
	}
}

func (b *spannerBroadcaster) Broadcast(event storage.ResourceEvent) {
}

func (b *spannerBroadcaster) Subscribe(sinceResourceVersion string) (<-chan storage.ResourceEvent, func(), error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, nil, fmt.Errorf("broadcaster is closed")
	}

	id := b.nextID
	b.nextID++

	// Internal channel receives live events immediately
	liveCh := make(chan storage.ResourceEvent, 100)
	b.subscribers[id] = liveCh
	b.mu.Unlock()

	// Output channel returned to caller — events arrive in order
	outCh := make(chan storage.ResourceEvent, 100)

	go func() {
		defer close(outCh)

		var lastReplayedRV int64
		if sinceResourceVersion != "" {
			var ok bool
			lastReplayedRV, ok = b.sendHistoricalEvents(outCh, sinceResourceVersion)
			if !ok {
				b.unsubscribe(id)
				return
			}
		}

		for event := range liveCh {
			if lastReplayedRV > 0 {
				rv, _ := strconv.ParseInt(event.ResourceVersion, 10, 64)
				if rv <= lastReplayedRV {
					continue
				}
			}
			select {
			case outCh <- event:
			default:
				b.unsubscribe(id)
				return
			}
		}
	}()

	stopFunc := func() {
		b.unsubscribe(id)
	}

	return outCh, stopFunc, nil
}

// sendHistoricalEvents replays events since the given RV. Returns the last
// replayed RV (for deduplication against live events) and false if the output
// channel is full (watch should be canceled).
func (b *spannerBroadcaster) sendHistoricalEvents(outCh chan storage.ResourceEvent, sinceResourceVersion string) (int64, bool) {
	rv, err := strconv.ParseInt(sinceResourceVersion, 10, 64)
	if err != nil {
		return 0, true
	}

	stmt := spanner.Statement{
		SQL: fmt.Sprintf(
			"SELECT resource_version, data, context_filter FROM %s WHERE resource_type = @resourceType AND resource_version > @sinceRV ORDER BY resource_version ASC LIMIT 1000",
			b.tableName,
		),
		Params: map[string]any{
			"resourceType": b.resourceType,
			"sinceRV":      rv,
		},
	}

	iter := b.client.Single().Query(b.ctx, stmt)
	defer iter.Stop()

	var lastRV int64
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return lastRV, true
		}

		var eventRV int64
		var objectDataJSON spanner.NullJSON
		var contextFilter string
		if err := row.Columns(&eventRV, &objectDataJSON, &contextFilter); err != nil {
			continue
		}

		objectDataBytes, err := json.Marshal(objectDataJSON.Value)
		if err != nil {
			continue
		}

		obj, err := b.reconstructObject(objectDataBytes)
		if err != nil {
			continue
		}

		obj.SetResourceVersion(strconv.FormatInt(eventRV, 10))

		event := storage.ResourceEvent{
			Type:               storage.EventAdded,
			ResourceVersion:    strconv.FormatInt(eventRV, 10),
			Object:             obj,
			ContextFilterValue: contextFilter,
		}

		select {
		case outCh <- event:
			lastRV = eventRV
		default:
			return lastRV, false
		}
	}
	return lastRV, true
}

func (b *spannerBroadcaster) unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if ch, exists := b.subscribers[id]; exists {
		close(ch)
		delete(b.subscribers, id)
	}
}

func (b *spannerBroadcaster) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}

	b.closed = true
	b.cancel()

	for id, ch := range b.subscribers {
		close(ch)
		delete(b.subscribers, id)
	}
	b.mu.Unlock()

	b.wg.Wait()

	return nil
}

var _ storage.EventBroadcaster = (*spannerBroadcaster)(nil)
