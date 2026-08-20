// Package firestore implements transport.Client using Google Cloud Firestore
// as the transport layer via kube-applier-gcp desire documents.
package firestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"

	"github.com/openshift-online/gecko/controllers/client/transport"
	"github.com/openshift-online/gecko/controllers/util/logger"
	"github.com/openshift-online/kube-applier-gcp/pkg/api/kubeapplier"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
)

const (
	collectionApplyDesires  = "applydesires"
	collectionReadDesires   = "readdesires"
	collectionDeleteDesires = "deletedesires"
)

const (
	specsDBName  = "specs"
	statusDBName = "status"

	// maxDeleteBatchSize is the maximum number of resources to delete per
	// Firestore transaction. Each resource requires 3 writes (Set DeleteDesire,
	// Delete ReadDesire, Delete ApplyDesire) and Firestore limits transactions
	// to 500 writes.
	maxDeleteBatchSize = 166
)

// mcClients caches the Firestore client pair for one management cluster.
type mcClients struct {
	specs  *firestore.Client // specs DB (apply/read/delete desires written here)
	status *firestore.Client // status DB (status read back from here)
}

// Client implements transport.Client using Firestore as the transport.
// One pair of *firestore.Client is maintained per management cluster.
// MCs are identified by their GCP project ID.
type Client struct {
	mu    sync.RWMutex
	cache map[string]*mcClients
	log   logger.Logger
	// dialOpts are extra grpc/firestore client options, used to inject emulator settings in tests.
	dialOpts []option.ClientOption
}

// Ensure Client implements transport.Client.
var _ transport.Client = (*Client)(nil)

// New creates a new Firestore transport client.
// The management cluster name passed to Apply/GetStatus/Delete is used directly
// as the GCP project ID.
// Use opts to inject emulator settings in tests (e.g. option.WithEndpoint).
func New(log logger.Logger, opts ...option.ClientOption) *Client {
	return &Client{
		cache:    make(map[string]*mcClients),
		log:      log,
		dialOpts: opts,
	}
}

// clients returns (or lazily creates) the Firestore client pair for the given MC.
func (c *Client) clients(ctx context.Context, mcName string) (*mcClients, error) {
	c.mu.RLock()
	if mc, ok := c.cache[mcName]; ok {
		c.mu.RUnlock()
		return mc, nil
	}
	c.mu.RUnlock()

	// Construct clients outside the lock to avoid blocking other goroutines
	// during gRPC dialing. MCs are identified by their GCP project ID.
	specsClient, err := firestore.NewClientWithDatabase(ctx, mcName, specsDBName, c.dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("firestore transport: create specs client for MC %q: %w", mcName, err)
	}

	statusClient, err := firestore.NewClientWithDatabase(ctx, mcName, statusDBName, c.dialOpts...)
	if err != nil {
		specsClient.Close() //nolint:errcheck
		return nil, fmt.Errorf("firestore transport: create status client for MC %q: %w", mcName, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check after acquiring write lock — another goroutine may have populated
	// the cache while we were dialing. Close the duplicate pair if so.
	if mc, ok := c.cache[mcName]; ok {
		specsClient.Close()   //nolint:errcheck
		statusClient.Close()  //nolint:errcheck
		return mc, nil
	}

	mc := &mcClients{specs: specsClient, status: statusClient}
	c.cache[mcName] = mc
	return mc, nil
}

// Apply decomposes the manifests into individual resources and writes one
// ApplyDesire + one ReadDesire document per resource to the specs DB.
// Returns the current status by calling GetStatus after writing.
func (c *Client) Apply(ctx context.Context, targetCluster, clusterID string, manifests [][]byte) (*transport.Status, error) {
	mc, err := c.clients(ctx, targetCluster)
	if err != nil {
		return nil, err
	}

	batch := mc.specs.BulkWriter(ctx)

	// Track jobs per document type so we can extract write timestamps.
	type writtenDoc struct {
		docID string
		job   *firestore.BulkWriterJob
	}
	var applyDocs, readDocs []writtenDoc

	for _, raw := range manifests {
		if len(raw) == 0 {
			continue
		}

		ref, unknownKind, err := parseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("firestore transport: Apply %s/%s: %w", targetCluster, clusterID, err)
		}
		if unknownKind {
			c.log.Infof(ctx, "firestore transport: Apply %s/%s: unknown Kind for resource %s/%s — using fallback pluralization, add it to kindToResource if incorrect", targetCluster, clusterID, ref.Namespace, ref.Name)
		}

		// Write ApplyDesire
		applyID, applyData, err := buildApplyDesireDoc(clusterID, targetCluster, ref, raw)
		if err != nil {
			return nil, fmt.Errorf("firestore transport: Apply %s/%s build apply desire: %w", targetCluster, clusterID, err)
		}
		applyRef := mc.specs.Collection(collectionApplyDesires).Doc(applyID)
		job, err := batch.Set(applyRef, applyData)
		if err != nil {
			return nil, fmt.Errorf("firestore transport: Apply %s/%s set apply desire: %w", targetCluster, clusterID, err)
		}
		applyDocs = append(applyDocs, writtenDoc{docID: applyID, job: job})

		// Write ReadDesire
		readID, readData := buildReadDesireDoc(clusterID, targetCluster, ref)
		readRef := mc.specs.Collection(collectionReadDesires).Doc(readID)
		job, err = batch.Set(readRef, readData)
		if err != nil {
			return nil, fmt.Errorf("firestore transport: Apply %s/%s set read desire: %w", targetCluster, clusterID, err)
		}
		readDocs = append(readDocs, writtenDoc{docID: readID, job: job})
	}

	batch.Flush()

	// Collect write timestamps from job results for staleness detection.
	writeTimes := make(map[string]time.Time, len(applyDocs)+len(readDocs))
	for _, wd := range applyDocs {
		wr, err := wd.job.Results()
		if err != nil {
			return nil, fmt.Errorf("firestore transport: Apply %s/%s write error: %w", targetCluster, clusterID, err)
		}
		writeTimes[collectionApplyDesires+"/"+wd.docID] = wr.UpdateTime
	}
	for _, wd := range readDocs {
		wr, err := wd.job.Results()
		if err != nil {
			return nil, fmt.Errorf("firestore transport: Apply %s/%s write error: %w", targetCluster, clusterID, err)
		}
		writeTimes[collectionReadDesires+"/"+wd.docID] = wr.UpdateTime
	}

	c.log.Infof(ctx, "firestore transport: applied %d manifests for %s/%s", len(manifests), targetCluster, clusterID)

	return c.getStatus(ctx, targetCluster, clusterID, writeTimes)
}

// GetStatus looks up document IDs from the specs DB by clusterID, then fetches
// the corresponding status documents from the status DB by document ID.
// This two-step approach is needed because kube-applier-gcp does not copy the
// spec fields into the status DB — only the document IDs match across DBs.
func (c *Client) GetStatus(ctx context.Context, targetCluster, clusterID string) (*transport.Status, error) {
	return c.getStatus(ctx, targetCluster, clusterID, nil)
}

// getStatus is the internal implementation of GetStatus. When specWriteTimes is
// non-nil (i.e. called from Apply), each desire's ObservedDesireUpdateTime is
// compared against the write timestamp to detect stale status.
func (c *Client) getStatus(ctx context.Context, targetCluster, clusterID string, specWriteTimes map[string]time.Time) (*transport.Status, error) {
	mc, err := c.clients(ctx, targetCluster)
	if err != nil {
		return nil, err
	}

	// Step 1: Query specs DB to discover document IDs for this clusterID.
	specsApplySnaps, err := mc.specs.Collection(collectionApplyDesires).
		Where("spec.clusterID", "==", clusterID).
		Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("firestore transport: GetStatus %s/%s query specs apply desires: %w", targetCluster, clusterID, err)
	}

	specsReadSnaps, err := mc.specs.Collection(collectionReadDesires).
		Where("spec.clusterID", "==", clusterID).
		Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("firestore transport: GetStatus %s/%s query specs read desires: %w", targetCluster, clusterID, err)
	}

	// Step 2: Fetch the corresponding documents from the status DB by ID.
	// GetAll preserves input order, so statusSnaps[i] corresponds to specsSnaps[i].
	// The spec fields in status docs are empty (kube-applier-gcp doesn't copy them),
	// so we take the spec from the specs DB snapshot instead.
	stale := false
	applyDesires := make([]kubeapplier.ApplyDesire, 0, len(specsApplySnaps))
	if len(specsApplySnaps) > 0 {
		applyRefs := make([]*firestore.DocumentRef, len(specsApplySnaps))
		for i, snap := range specsApplySnaps {
			applyRefs[i] = mc.status.Collection(collectionApplyDesires).Doc(snap.Ref.ID)
		}
		statusApplySnaps, err := mc.status.GetAll(ctx, applyRefs)
		if err != nil {
			return nil, fmt.Errorf("firestore transport: GetStatus %s/%s fetch status apply desires: %w", targetCluster, clusterID, err)
		}
		for i, snap := range statusApplySnaps {
			// Spec from status DB is empty — use the spec from the specs DB.
			var specsAD kubeapplier.ApplyDesire
			if err := specsApplySnaps[i].DataTo(&specsAD); err != nil {
				return nil, fmt.Errorf("firestore transport: GetStatus %s/%s decode specs apply desire %s: %w", targetCluster, clusterID, specsApplySnaps[i].Ref.ID, err)
			}
			if !snap.Exists() {
				// kube-applier-gcp has not created the status doc yet; report it as pending.
				applyDesires = append(applyDesires, kubeapplier.ApplyDesire{Spec: specsAD.Spec})
				stale = stale || specWriteTimes != nil
				continue
			}
			var ad kubeapplier.ApplyDesire
			if err := snap.DataTo(&ad); err != nil {
				return nil, fmt.Errorf("firestore transport: GetStatus %s/%s decode apply desire %s: %w", targetCluster, clusterID, snap.Ref.ID, err)
			}
			ad.Spec = specsAD.Spec
			// Check staleness: compare write timestamp against ObservedDesireUpdateTime.
			if wt, ok := specWriteTimes[collectionApplyDesires+"/"+specsApplySnaps[i].Ref.ID]; ok {
				if ad.Status.ObservedDesireUpdateTime.Before(wt) {
					stale = true
				}
			}
			applyDesires = append(applyDesires, ad)
		}
	}

	readDesires := make([]kubeapplier.ReadDesire, 0, len(specsReadSnaps))
	if len(specsReadSnaps) > 0 {
		readRefs := make([]*firestore.DocumentRef, len(specsReadSnaps))
		for i, snap := range specsReadSnaps {
			readRefs[i] = mc.status.Collection(collectionReadDesires).Doc(snap.Ref.ID)
		}
		statusReadSnaps, err := mc.status.GetAll(ctx, readRefs)
		if err != nil {
			return nil, fmt.Errorf("firestore transport: GetStatus %s/%s fetch status read desires: %w", targetCluster, clusterID, err)
		}
		for i, snap := range statusReadSnaps {
			// Spec from status DB is empty — use the spec from the specs DB.
			var specsRD kubeapplier.ReadDesire
			if err := specsReadSnaps[i].DataTo(&specsRD); err != nil {
				return nil, fmt.Errorf("firestore transport: GetStatus %s/%s decode specs read desire %s: %w", targetCluster, clusterID, specsReadSnaps[i].Ref.ID, err)
			}
			if !snap.Exists() {
				// kube-applier-gcp has not created the status doc yet; report it as pending.
				readDesires = append(readDesires, kubeapplier.ReadDesire{Spec: specsRD.Spec})
				stale = stale || specWriteTimes != nil
				continue
			}
			var rd kubeapplier.ReadDesire
			if err := snap.DataTo(&rd); err != nil {
				return nil, fmt.Errorf("firestore transport: GetStatus %s/%s decode read desire %s: %w", targetCluster, clusterID, snap.Ref.ID, err)
			}
			rd.Spec = specsRD.Spec
			// Check staleness: compare write timestamp against ObservedDesireUpdateTime.
			if wt, ok := specWriteTimes[collectionReadDesires+"/"+specsReadSnaps[i].Ref.ID]; ok {
				if rd.Status.ObservedDesireUpdateTime.Before(wt) {
					stale = true
				}
			}
			// Manually decode status_kubeContent (stored as map[string]any at doc root).
			if v, ok := snap.Data()["status_kubeContent"]; ok && v != nil {
				raw, err := json.Marshal(v)
				if err != nil {
					return nil, fmt.Errorf("firestore transport: GetStatus marshal status_kubeContent: %w", err)
				}
				rd.Status.KubeContent = &k8sruntime.RawExtension{Raw: raw}
			}
			readDesires = append(readDesires, rd)
		}
	}

	resourceStatuses, err := extractResourceStatuses(readDesires)
	if err != nil {
		return nil, fmt.Errorf("firestore transport: GetStatus %s/%s: %w", targetCluster, clusterID, err)
	}

	return &transport.Status{
		Conditions:       aggregateConditions(applyDesires),
		ResourceStatuses: resourceStatuses,
		Stale:            stale,
	}, nil
}

// Delete writes one DeleteDesire document per resource and removes the
// corresponding ApplyDesire and ReadDesire documents from the specs DB.
// Resources are processed in batches to stay within Firestore's
// 500-write-per-transaction limit.
func (c *Client) Delete(ctx context.Context, targetCluster, clusterID string) error {
	mc, err := c.clients(ctx, targetCluster)
	if err != nil {
		return err
	}

	// Query all ApplyDesires for this clusterID.
	applySnaps, err := mc.specs.Collection(collectionApplyDesires).
		Where("spec.clusterID", "==", clusterID).
		Documents(ctx).GetAll()
	if err != nil {
		return fmt.Errorf("firestore transport: Delete %s/%s query apply desires: %w", targetCluster, clusterID, err)
	}

	if len(applySnaps) == 0 {
		c.log.Infof(ctx, "firestore transport: Delete %s/%s: no apply desires found, nothing to delete", targetCluster, clusterID)
		return nil
	}

	// Process in chunks to stay within Firestore's 500-write-per-transaction
	// limit. Each resource requires 3 writes (Set DeleteDesire, Delete
	// ReadDesire, Delete ApplyDesire).
	var errs []error
	for i := 0; i < len(applySnaps); i += maxDeleteBatchSize {
		end := i + maxDeleteBatchSize
		if end > len(applySnaps) {
			end = len(applySnaps)
		}
		chunk := applySnaps[i:end]

		err := mc.specs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
			for _, snap := range chunk {
				var ad kubeapplier.ApplyDesire
				if err := snap.DataTo(&ad); err != nil {
					return fmt.Errorf("decode apply desire: %w", err)
				}

				ref := ad.Spec.TargetItem
				taskKey := ad.Spec.ClusterID

				// Write DeleteDesire.
				deleteID, deleteData := buildDeleteDesireDoc(taskKey, targetCluster, ref)
				deleteRef := mc.specs.Collection(collectionDeleteDesires).Doc(deleteID)
				if err := tx.Set(deleteRef, deleteData); err != nil {
					return fmt.Errorf("set delete desire: %w", err)
				}

				// Delete the ReadDesire doc (same document ID as the ApplyDesire).
				readRef := mc.specs.Collection(collectionReadDesires).Doc(snap.Ref.ID)
				if err := tx.Delete(readRef); err != nil {
					return fmt.Errorf("delete read desire: %w", err)
				}

				// Delete the ApplyDesire doc.
				if err := tx.Delete(snap.Ref); err != nil {
					return fmt.Errorf("delete apply desire: %w", err)
				}
			}
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("chunk %d-%d: %w", i, end-1, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("firestore transport: Delete %s/%s: %d chunk(s) failed: %w", targetCluster, clusterID, len(errs), errors.Join(errs...))
	}

	c.log.Infof(ctx, "firestore transport: deleted %d resources for %s/%s", len(applySnaps), targetCluster, clusterID)
	return nil
}

// GetDeleteStatus queries all DeleteDesire documents for the given clusterID
// and checks if all have Successful=True condition.
func (c *Client) GetDeleteStatus(ctx context.Context, targetCluster, clusterID string) (*transport.DeleteStatus, error) {
	mc, err := c.clients(ctx, targetCluster)
	if err != nil {
		return nil, err
	}

	// Query all DeleteDesires from specs DB.
	specsSnaps, err := mc.specs.Collection(collectionDeleteDesires).
		Where("spec.clusterID", "==", clusterID).
		Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("firestore transport: GetDeleteStatus %s/%s query specs: %w", targetCluster, clusterID, err)
	}

	// Also query ApplyDesires to distinguish "never started" from "completed".
	applySnaps, err := mc.specs.Collection(collectionApplyDesires).
		Where("spec.clusterID", "==", clusterID).
		Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("firestore transport: GetDeleteStatus %s/%s query apply desires: %w", targetCluster, clusterID, err)
	}

	if len(specsSnaps) == 0 {
		// No DeleteDesires found.
		// If ApplyDesires exist, deletion never started (controller must call Delete).
		// If no ApplyDesires, deletion completed.
		return &transport.DeleteStatus{
			AllSuccessful:     len(applySnaps) == 0,
			PendingCount:      0,
			TotalCount:        0,
			ApplyDesiresCount: len(applySnaps),
		}, nil
	}

	// Fetch corresponding status documents.
	statusRefs := make([]*firestore.DocumentRef, len(specsSnaps))
	for i, snap := range specsSnaps {
		statusRefs[i] = mc.status.Collection(collectionDeleteDesires).Doc(snap.Ref.ID)
	}
	statusSnaps, err := mc.status.GetAll(ctx, statusRefs)
	if err != nil {
		return nil, fmt.Errorf("firestore transport: GetDeleteStatus %s/%s fetch status: %w", targetCluster, clusterID, err)
	}

	pending := 0
	for _, snap := range statusSnaps {
		if !snap.Exists() {
			// kube-applier-gcp hasn't processed this DeleteDesire yet.
			pending++
			continue
		}
		var dd kubeapplier.DeleteDesire
		if err := snap.DataTo(&dd); err != nil {
			return nil, fmt.Errorf("firestore transport: GetDeleteStatus %s/%s decode: %w", targetCluster, clusterID, err)
		}
		// Check for Successful=True condition.
		successful := false
		for _, cond := range dd.Status.Conditions {
			if cond.Type == kubeapplier.ConditionTypeSuccessful && cond.Status == "True" {
				successful = true
				break
			}
		}
		if !successful {
			pending++
		}
	}

	return &transport.DeleteStatus{
		AllSuccessful:     pending == 0,
		PendingCount:      pending,
		TotalCount:        len(specsSnaps),
		ApplyDesiresCount: len(applySnaps),
	}, nil
}

// CleanupDeleteDesires removes all DeleteDesire documents for the given clusterID
// from both specs and status DBs.
func (c *Client) CleanupDeleteDesires(ctx context.Context, targetCluster, clusterID string) error {
	mc, err := c.clients(ctx, targetCluster)
	if err != nil {
		return err
	}

	// Query DeleteDesires from specs DB.
	snaps, err := mc.specs.Collection(collectionDeleteDesires).
		Where("spec.clusterID", "==", clusterID).
		Documents(ctx).GetAll()
	if err != nil {
		return fmt.Errorf("firestore transport: CleanupDeleteDesires %s/%s query: %w", targetCluster, clusterID, err)
	}

	if len(snaps) == 0 {
		c.log.Infof(ctx, "firestore transport: CleanupDeleteDesires %s/%s: no delete desires found", targetCluster, clusterID)
		return nil
	}

	// Delete in batches (Firestore batch write limit = 500).
	// Use separate BulkWriters: specs and status refs have same shortPath (deletedesires/<id>),
	// BulkWriter rejects duplicate paths.
	specsBatch := mc.specs.BulkWriter(ctx)
	statusBatch := mc.status.BulkWriter(ctx)
	var specsJobs []*firestore.BulkWriterJob
	var statusJobs []*firestore.BulkWriterJob

	for _, snap := range snaps {
		// Delete from specs DB.
		job, err := specsBatch.Delete(snap.Ref)
		if err != nil {
			return fmt.Errorf("firestore transport: CleanupDeleteDesires %s/%s delete specs: %w", targetCluster, clusterID, err)
		}
		specsJobs = append(specsJobs, job)

		// Delete from status DB (best-effort — may not exist yet).
		statusRef := mc.status.Collection(collectionDeleteDesires).Doc(snap.Ref.ID)
		job, err = statusBatch.Delete(statusRef)
		if err != nil {
			return fmt.Errorf("firestore transport: CleanupDeleteDesires %s/%s delete status: %w", targetCluster, clusterID, err)
		}
		statusJobs = append(statusJobs, job)
	}

	specsBatch.Flush()
	statusBatch.Flush()

	// Check job results.
	for _, job := range specsJobs {
		if _, err := job.Results(); err != nil {
			return fmt.Errorf("firestore transport: CleanupDeleteDesires %s/%s specs write error: %w", targetCluster, clusterID, err)
		}
	}
	for _, job := range statusJobs {
		if _, err := job.Results(); err != nil {
			return fmt.Errorf("firestore transport: CleanupDeleteDesires %s/%s status write error: %w", targetCluster, clusterID, err)
		}
	}

	c.log.Infof(ctx, "firestore transport: cleaned up %d delete desires for %s/%s", len(snaps), targetCluster, clusterID)
	return nil
}

// Close closes all cached Firestore clients. Call on shutdown.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, mc := range c.cache {
		mc.specs.Close()  //nolint:errcheck
		mc.status.Close() //nolint:errcheck
	}
	c.cache = make(map[string]*mcClients)
}
