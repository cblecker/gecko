package versionresolution

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openshift-online/gecko/controllers/util/logger"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	adapterName         = "version-resolution-controller"
	defaultChannelGroup = "candidate"
	requeueStable       = 5 * time.Minute
)

// Reconciler resolves the OCP release image for a cluster via Cincinnati.
type Reconciler struct {
	cincinnati *CincinnatiClient
	log        logger.Logger
	client     client.Client
}

// NewReconciler creates a new version-resolution Reconciler.
func NewReconciler(cincinnati *CincinnatiClient, log logger.Logger, c client.Client) *Reconciler {
	return &Reconciler{
		cincinnati: cincinnati,
		log:        log,
		client:     c,
	}
}

// Reconcile runs the version-resolution loop for one cluster event.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	clusterID := req.Name

	var cluster privatev1.Cluster
	if err := r.client.Get(ctx, req.NamespacedName, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			r.log.Infof(ctx, "vr: cluster %s not found, skipping", clusterID)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("vr: get cluster %s: %w", clusterID, err)
	}

	if cluster.Spec.Release.Version == "" {
		r.log.Infof(ctx, "vr: cluster %s: release version not set, waiting for next event", clusterID)
		if meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:               "VersionResolved",
			Status:             metav1.ConditionUnknown,
			Reason:             "ReleaseVersionNotSet",
			Message:            "Release version not set in spec",
			ObservedGeneration: cluster.Generation,
		}) {
			if err := r.client.Status().Update(ctx, &cluster); err != nil && !apierrors.IsConflict(err) {
				return reconcile.Result{}, fmt.Errorf("vr: update cluster status %s: %w", clusterID, err)
			}
		}
		return reconcile.Result{}, nil
	}
	version := cluster.Spec.Release.Version

	channelGroup := defaultChannelGroup
	if cluster.Spec.Release.ChannelGroup != "" {
		channelGroup = cluster.Spec.Release.ChannelGroup
	}
	channel, err := buildChannel(version, channelGroup)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("vr: build channel for cluster %s: %w", clusterID, err)
	}

	// Check already resolved — both version and channel group must match to skip re-resolution.
	if vr := cluster.Status.VersionResolution; vr != nil &&
		vr.ReleaseVersion == version &&
		vr.ChannelGroup == channelGroup {
		r.log.Infof(ctx, "vr: cluster %s: version %s channel %s already resolved, waiting for next event", clusterID, version, channelGroup)
		return reconcile.Result{}, nil
	}
	r.log.Infof(ctx, "vr: cluster %s: resolving version %s via channel %s", clusterID, version, channel)

	info, err := r.cincinnati.Resolve(ctx, version, channel)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("vr: cincinnati resolve for cluster %s: %w", clusterID, err)
	}
	if info == nil {
		r.log.Warnf(ctx, "vr: cluster %s: version %s not found in Cincinnati, waiting for next event", clusterID, version)
		if meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:               "VersionResolved",
			Status:             metav1.ConditionFalse,
			Reason:             "VersionNotFoundInCincinnati",
			Message:            fmt.Sprintf("Version %s not found in Cincinnati channel %s", version, channel),
			ObservedGeneration: cluster.Generation,
		}) {
			if err := r.client.Status().Update(ctx, &cluster); err != nil && !apierrors.IsConflict(err) {
				return reconcile.Result{}, fmt.Errorf("vr: update cluster status %s: %w", clusterID, err)
			}
		}
		return reconcile.Result{}, nil
	}

	// Write VR result and VersionResolved condition to status.
	cluster.Status.VersionResolution = &privatev1.VersionResolutionResult{
		ReleaseImage:      info.Payload,
		ReleaseVersion:    info.Version,
		CincinnatiChannel: channel,
		ChannelGroup:      channelGroup,
	}
	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:               "VersionResolved",
		Status:             metav1.ConditionTrue,
		Reason:             "VersionResolved",
		Message:            fmt.Sprintf("Version %s resolved to image %s", version, info.Payload),
		ObservedGeneration: cluster.Generation,
	})
	if err := r.client.Status().Update(ctx, &cluster); err != nil {
		if apierrors.IsConflict(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("vr: update cluster status %s: %w", clusterID, err)
	}

	r.log.Infof(ctx, "vr: cluster %s: resolved version %s", clusterID, version)
	return reconcile.Result{RequeueAfter: requeueStable}, nil
}

// buildChannel constructs the Cincinnati channel name from a version string and channel group.
// e.g. "4.22.0-ec.4" + "stable" → "stable-4.22"
func buildChannel(version, channelGroup string) (string, error) {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid version %q: expected at least major.minor", version)
	}
	return fmt.Sprintf("%s-%s.%s", channelGroup, parts[0], parts[1]), nil
}
