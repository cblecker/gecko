// Package hc implements the hc-controller reconciler for managing HostedClusters via kube-applier-gcp.
package hc

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	"github.com/openshift-online/gecko/controllers/client/transport"
	"github.com/openshift-online/gecko/controllers/hc/manifest"
	"github.com/openshift-online/gecko/controllers/util/constants"
	"github.com/openshift-online/gecko/controllers/util/logger"
)

const (
	adapterName = "hc-controller"

	requeuePending = 15 * time.Second
	requeueStable  = 5 * time.Minute
)

// Reconciler implements the hc-controller reconcile loop.
type Reconciler struct {
	transport transport.Client
	log       logger.Logger
	client    client.Client
}

// New creates a new Reconciler.
func New(transport transport.Client, log logger.Logger, c client.Client) *Reconciler {
	return &Reconciler{
		transport: transport,
		log:       log,
		client:    c,
	}
}

// Reconcile runs the hc-controller loop for one cluster event.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	clusterID := req.Name
	log := r.log.With("controller", adapterName).With("cluster_id", clusterID)

	var cluster privatev1.Cluster
	if err := r.client.Get(ctx, req.NamespacedName, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			log.Infof(ctx, "cluster not found, skipping")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("%s: get cluster: %w", adapterName, err)
	}

	// Handle deletion.
	if !cluster.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &cluster, log)
	}

	// Ensure finalizer is present.
	if !controllerutil.ContainsFinalizer(&cluster, constants.FinalizerCluster) {
		controllerutil.AddFinalizer(&cluster, constants.FinalizerCluster)
		if err := r.client.Update(ctx, &cluster); err != nil {
			return reconcile.Result{}, fmt.Errorf("%s: add finalizer: %w", adapterName, err)
		}
		return reconcile.Result{}, nil // re-reconcile with finalizer in place
	}

	// Check placement readiness.
	if cluster.Status.PlacementResult == nil || cluster.Status.PlacementResult.ManagementClusterName == "" {
		if r.setWaitingConditions(&cluster, "PlacementNotReady", "Waiting for placement to select a management cluster") {
			if err := r.client.Status().Update(ctx, &cluster); err != nil && !apierrors.IsConflict(err) {
				return reconcile.Result{}, fmt.Errorf("%s: update cluster status: %w", adapterName, err)
			}
		}
		log.Infof(ctx, "placement not ready, requeueing after %s", requeuePending)
		return reconcile.Result{RequeueAfter: requeuePending}, nil
	}

	// Check version-resolution readiness.
	if cluster.Status.VersionResolution == nil {
		if r.setWaitingConditions(&cluster, "VersionResolutionNotReady", "Waiting for version resolution") {
			if err := r.client.Status().Update(ctx, &cluster); err != nil && !apierrors.IsConflict(err) {
				return reconcile.Result{}, fmt.Errorf("%s: update cluster status: %w", adapterName, err)
			}
		}
		log.Infof(ctx, "version resolution not ready, requeueing after %s", requeuePending)
		return reconcile.Result{RequeueAfter: requeuePending}, nil
	}

	// Check version match.
	if cluster.Status.VersionResolution.ReleaseVersion != cluster.Spec.Release.Version {
		msg := fmt.Sprintf("VR version %q does not match spec version %q",
			cluster.Status.VersionResolution.ReleaseVersion, cluster.Spec.Release.Version)
		if r.setWaitingConditions(&cluster, "VersionMismatch", msg) {
			if err := r.client.Status().Update(ctx, &cluster); err != nil && !apierrors.IsConflict(err) {
				return reconcile.Result{}, fmt.Errorf("%s: update cluster status: %w", adapterName, err)
			}
		}
		log.Infof(ctx, "vr version %q does not match spec version %q, requeueing after %s",
			cluster.Status.VersionResolution.ReleaseVersion, cluster.Spec.Release.Version, requeuePending)
		return reconcile.Result{RequeueAfter: requeuePending}, nil
	}

	placement := cluster.Status.PlacementResult
	vr := cluster.Status.VersionResolution

	// Extract platform fields.
	var gcpProjectID, gcpRegion, gcpNetwork, gcpSubnet, gcpEndpointAccess string
	var wifProjectNumber, wifPoolID, wifProviderID string
	var nodePoolEmail, controlPlaneEmail, cloudControllerEmail string
	var storageEmail, imageRegistryEmail, networkEmail string
	if gcp := cluster.Spec.Platform.GCP; gcp != nil {
		gcpProjectID = gcp.ProjectID
		gcpRegion = gcp.Region
		gcpNetwork = gcp.Network
		gcpSubnet = gcp.Subnet
		gcpEndpointAccess = gcp.EndpointAccess
		wif := gcp.WorkloadIdentity
		wifProjectNumber = wif.ProjectNumber
		wifPoolID = wif.PoolID
		wifProviderID = wif.ProviderID
		if sa := wif.ServiceAccountsRef; sa != nil {
			nodePoolEmail = sa.NodePoolEmail
			controlPlaneEmail = sa.ControlPlaneEmail
			cloudControllerEmail = sa.CloudControllerEmail
			storageEmail = sa.StorageEmail
			imageRegistryEmail = sa.ImageRegistryEmail
			networkEmail = sa.NetworkEmail
		}
	}

	// Build manifests.
	// TODO: ClusterIDUUID and CreatedBy are not yet in the orlop ClusterSpec.
	mwInput := manifest.Input{
		ClusterID:            clusterID,
		ClusterName:          cluster.Name,
		Generation:           cluster.Generation,
		CreatedBy:            cluster.Annotations[constants.AnnotationCreatedBy],
		InfraID:              cluster.Spec.InfraID,
		IssuerURL:            cluster.Spec.IssuerURL,
		ClusterIDUUID:        string(cluster.UID),
		GCPProjectID:         gcpProjectID,
		GCPRegion:            gcpRegion,
		GCPNetwork:           gcpNetwork,
		GCPSubnet:            gcpSubnet,
		GCPEndpointAccess:    gcpEndpointAccess,
		WIFProjectNumber:     wifProjectNumber,
		WIFPoolID:            wifPoolID,
		WIFProviderID:        wifProviderID,
		NodePoolEmail:        nodePoolEmail,
		ControlPlaneEmail:    controlPlaneEmail,
		CloudControllerEmail: cloudControllerEmail,
		StorageEmail:         storageEmail,
		ImageRegistryEmail:   imageRegistryEmail,
		NetworkEmail:         networkEmail,
		ReleaseImage:         vr.ReleaseImage,
		ReleaseChannel:       vr.CincinnatiChannel,
		BaseDomain:           placement.BaseDomain,
	}

	manifests, err := manifest.Build(mwInput)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("%s: build manifests: %w", adapterName, err)
	}

	mwStatus, err := r.transport.Apply(ctx, placement.ManagementClusterName, clusterID, manifests)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("%s: apply resources: %w", adapterName, err)
	}

	// If status is stale, skip condition updates and requeue quickly.
	if mwStatus != nil && mwStatus.Stale {
		log.Infof(ctx, "hc-controller: cluster %s status is stale, requeueing after %s", clusterID, requeuePending)
		return reconcile.Result{RequeueAfter: requeuePending}, nil
	}

	// Write status conditions — only update if something changed.
	if r.applyStatusConditions(&cluster, mwStatus) {
		if err := r.client.Status().Update(ctx, &cluster); err != nil {
			if apierrors.IsConflict(err) {
				return reconcile.Result{}, nil
			}
			return reconcile.Result{}, fmt.Errorf("%s: update cluster status: %w", adapterName, err)
		}
	}

	if !meta.IsStatusConditionTrue(cluster.Status.Conditions, "ResourcesApplied") {
		log.Infof(ctx, "hc-controller: cluster %s resources not yet applied, requeueing after %s", clusterID, requeuePending)
		return reconcile.Result{RequeueAfter: requeuePending}, nil
	}
	log.Infof(ctx, "hc-controller: cluster %s reconciled, requeueing after %s", clusterID, requeueStable)
	return reconcile.Result{RequeueAfter: requeueStable}, nil
}

// handleDeletion cleans up management-cluster resources and removes the finalizer.
// Deletion flow:
// 1. Call transport.Delete to enqueue DeleteDesires (async)
// 2. Requeue, wait for kube-applier-gcp to process (poll GetDeleteStatus)
// 3. Once all DeleteDesires report Successful=True, cleanup DeleteDesires
// 4. Remove finalizer
func (r *Reconciler) handleDeletion(ctx context.Context, cluster *privatev1.Cluster, log logger.Logger) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(cluster, constants.FinalizerCluster) {
		return reconcile.Result{}, nil
	}

	clusterID := cluster.Name

	// Only call transport.Delete if resources were applied to an MC.
	if meta.FindStatusCondition(cluster.Status.Conditions, "ResourcesApplied") != nil &&
		cluster.Status.PlacementResult != nil && cluster.Status.PlacementResult.ManagementClusterName != "" {
		mcName := cluster.Status.PlacementResult.ManagementClusterName

		// Check if deletion already in progress by querying delete status first.
		deleteStatus, err := r.transport.GetDeleteStatus(ctx, mcName, clusterID)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("%s: get delete status: %w", adapterName, err)
		}

		if deleteStatus.TotalCount == 0 {
			// No DeleteDesires exist.
			if deleteStatus.ApplyDesiresCount > 0 {
				// ApplyDesires still present → deletion never started, call Delete().
				log.Infof(ctx, "%s: deleting resources for cluster %s from %s", adapterName, clusterID, mcName)
				if err := r.transport.Delete(ctx, mcName, clusterID); err != nil {
					return reconcile.Result{}, fmt.Errorf("%s: delete resources: %w", adapterName, err)
				}
				log.Infof(ctx, "%s: delete initiated for cluster %s, requeueing to poll status", adapterName, clusterID)
				return reconcile.Result{RequeueAfter: requeuePending}, nil
			}
			// TotalCount=0 and ApplyDesiresCount=0 → deletion already complete (no-op), proceed to finalizer.
		}

		if !deleteStatus.AllSuccessful {
			// Deletion in progress — wait for completion.
			log.Infof(ctx, "%s: deletion in progress for cluster %s (%d/%d pending), requeueing",
				adapterName, clusterID, deleteStatus.PendingCount, deleteStatus.TotalCount)
			return reconcile.Result{RequeueAfter: requeuePending}, nil
		}

		// All DeleteDesires successful — cleanup before removing finalizer.
		log.Infof(ctx, "%s: deletion complete for cluster %s, cleaning up %d DeleteDesires",
			adapterName, clusterID, deleteStatus.TotalCount)
		if err := r.transport.CleanupDeleteDesires(ctx, mcName, clusterID); err != nil {
			return reconcile.Result{}, fmt.Errorf("%s: cleanup delete desires: %w", adapterName, err)
		}
	}

	controllerutil.RemoveFinalizer(cluster, constants.FinalizerCluster)
	if err := r.client.Update(ctx, cluster); err != nil {
		return reconcile.Result{}, fmt.Errorf("%s: remove finalizer: %w", adapterName, err)
	}

	log.Infof(ctx, "%s: finalizer removed for cluster %s", adapterName, clusterID)
	return reconcile.Result{}, nil
}

// setWaitingConditions sets ResourcesApplied and HostedClusterAvailable to Unknown.
// Returns true if either condition changed.
func (r *Reconciler) setWaitingConditions(cluster *privatev1.Cluster, reason, message string) bool {
	gen := cluster.Generation
	a := meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:               "ResourcesApplied",
		Status:             metav1.ConditionUnknown,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: gen,
	})
	b := meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:               "HostedClusterAvailable",
		Status:             metav1.ConditionUnknown,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: gen,
	})
	return a || b
}

// applyStatusConditions derives conditions from the resource status and writes them to the cluster.
// Returns true if any condition changed.
func (r *Reconciler) applyStatusConditions(cluster *privatev1.Cluster, mwStatus *transport.Status) bool {
	gen := cluster.Generation

	if mwStatus == nil {
		a := meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:               "ResourcesApplied",
			Status:             metav1.ConditionFalse,
			Reason:             "ResourcesNotFound",
			Message:            "Resources have not been applied yet",
			ObservedGeneration: gen,
		})
		b := meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:               "HostedClusterAvailable",
			Status:             metav1.ConditionFalse,
			Reason:             "ResourcesNotFound",
			Message:            "Resources have not been applied yet",
			ObservedGeneration: gen,
		})
		c := meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:               "ApiCertificateReady",
			Status:             metav1.ConditionFalse,
			Reason:             "ResourcesNotFound",
			Message:            "Resources have not been applied yet",
			ObservedGeneration: gen,
		})
		return a || b || c
	}

	// Derive ResourcesApplied from top-level conditions.
	appliedStatus, appliedReason, appliedMessage := firstCondition(mwStatus.Conditions, "Applied")

	// Derive HostedClusterAvailable and HostedClusterResult fields from HC resource status.
	hcKey := transport.ResourceKey(constants.HyperShiftGroup, constants.HyperShiftVersion, "hostedclusters",
		fmt.Sprintf("clusters-%s", cluster.Name), cluster.Name)
	availableStatus := string(metav1.ConditionFalse)
	apiEndpoint := ""
	version := ""
	if hcFeedback, ok := mwStatus.ResourceStatuses[hcKey]; ok {
		if v, ok := hcFeedback["availableCondition"]; ok {
			availableStatus = v
		}
		if v, ok := hcFeedback["controlPlaneEndpoint"]; ok {
			apiEndpoint = v
		}
		if v, ok := hcFeedback["version"]; ok {
			version = v
		}
	}

	// Derive ApiCertificateReady from Certificate resource status.
	clusterNS := fmt.Sprintf("clusters-%s", cluster.Name)
	certKey := transport.ResourceKey("cert-manager.io", "v1", "certificates", clusterNS, "external-api-cert")
	certStatus := string(metav1.ConditionFalse)
	certReason := "CertificateNotReady"
	certMessage := ""
	if certFeedback, ok := mwStatus.ResourceStatuses[certKey]; ok {
		if v, ok := certFeedback["readyCondition"]; ok {
			// Validate condition value - only accept True/False/Unknown
			if v == string(metav1.ConditionTrue) || v == string(metav1.ConditionFalse) || v == string(metav1.ConditionUnknown) {
				certStatus = v
				if v == string(metav1.ConditionTrue) {
					certReason = "CertificateReady"
				}
			}
		}
	}

	a := meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:               "ResourcesApplied",
		Status:             metav1.ConditionStatus(appliedStatus),
		Reason:             appliedReason,
		Message:            appliedMessage,
		ObservedGeneration: gen,
	})
	b := meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:               "HostedClusterAvailable",
		Status:             metav1.ConditionStatus(availableStatus),
		Reason:             "HostedClusterAvailable",
		ObservedGeneration: gen,
	})
	d := meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:               "ApiCertificateReady",
		Status:             metav1.ConditionStatus(certStatus),
		Reason:             certReason,
		Message:            certMessage,
		ObservedGeneration: gen,
	})

	// Write HostedClusterResult when either field is non-empty.
	c := false
	if apiEndpoint != "" || version != "" {
		desired := &privatev1.HostedClusterResult{
			APIEndpoint: apiEndpoint,
			Version:     version,
		}
		if cluster.Status.HostedClusterResult == nil ||
			cluster.Status.HostedClusterResult.APIEndpoint != desired.APIEndpoint ||
			cluster.Status.HostedClusterResult.Version != desired.Version {
			cluster.Status.HostedClusterResult = desired
			c = true
		}
	}

	return a || b || c || d
}

// firstCondition returns the status, reason, and message of the first condition matching condType.
// Defaults: status="False", reason="Unknown", message="".
func firstCondition(conds []metav1.Condition, condType string) (status, reason, message string) {
	for _, c := range conds {
		if c.Type == condType {
			return string(c.Status), c.Reason, c.Message
		}
	}
	return "False", "Unknown", ""
}
