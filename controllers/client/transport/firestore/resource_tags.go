package firestore

import (
	"context"
	"fmt"
	"reflect"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openshift-online/kube-applier-gcp/pkg/api/kubeapplier"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// applyHostedCluster preserves the first submitted tags, including their absence.
// Reading and writing atomically prevents competing reconciles from replacing the
// initial tags. This remembers submission, not successful Kubernetes creation.
func applyHostedCluster(ctx context.Context, db *firestore.Client, groupKey, mcName string, ref kubeapplier.ResourceReference, raw []byte) (bool, error) {
	var mismatch bool
	err := db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		// Transactions may retry: start with the original desired manifest each time.
		mismatch = false
		id, data, err := buildApplyDesireDoc(groupKey, mcName, ref, raw)
		if err != nil {
			return err
		}
		manifest := data["spec_kubeContent"].(map[string]any)
		doc := db.Collection(collectionApplyDesires).Doc(id)
		existing, err := tx.Get(doc)
		switch {
		case status.Code(err) == codes.NotFound:
			// First submission: use the generated tags.
		case err != nil:
			return err
		default:
			oldManifest, found, err := unstructured.NestedMap(existing.Data(), "spec_kubeContent")
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("existing HostedCluster desire has no manifest")
			}
			mismatch, err = preserveResourceTags(manifest, oldManifest)
			if err != nil {
				return err
			}
		}
		// Validate the selected list, not the unpreserved desired union. An
		// existing cluster keeps its valid tags even if configuration adds keys.
		tags, _, err := unstructured.NestedSlice(manifest, "spec", "platform", "gcp", "resourceTags")
		if err != nil {
			return err
		}
		if len(tags) > 50 {
			return fmt.Errorf("HostedCluster resource tags must contain at most 50 entries")
		}
		readID, readData := buildReadDesireDoc(groupKey, mcName, ref)
		if err := tx.Set(doc, data); err != nil {
			return err
		}
		return tx.Set(db.Collection(collectionReadDesires).Doc(readID), readData)
	})
	return mismatch, err
}

// preserveResourceTags only changes tags; all other desired fields are retained.
func preserveResourceTags(desired, previous map[string]any) (bool, error) {
	path := []string{"spec", "platform", "gcp", "resourceTags"}
	oldTags, oldFound, err := unstructured.NestedFieldCopy(previous, path...)
	if err != nil {
		return false, err
	}
	newTags, newFound, err := unstructured.NestedFieldCopy(desired, path...)
	if err != nil {
		return false, err
	}
	mismatch := oldFound != newFound || !reflect.DeepEqual(oldTags, newTags)
	if !oldFound {
		unstructured.RemoveNestedField(desired, path...)
		return mismatch, nil
	}
	return mismatch, unstructured.SetNestedField(desired, oldTags, path...)
}
