package controlplaneupgrade

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMaintenancePermitted(t *testing.T) {
	window := &privatev1.ControlPlaneMaintenanceWindow{
		StartTime:       metav1.NewTime(time.Date(2026, time.October, 14, 23, 0, 0, 0, time.UTC)),
		DurationSeconds: 14400, // 4 hours
		Recurrence: privatev1.ControlPlaneMaintenanceRecurrence{
			Frequency:  "weekly",
			DaysOfWeek: []string{"wednesday"},
		},
	}
	exclusion := privatev1.ControlPlaneMaintenanceExclusion{
		Name:      "freeze",
		StartTime: metav1.NewTime(time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC)),
		EndTime:   metav1.NewTime(time.Date(2026, time.October, 15, 2, 0, 0, 0, time.UTC)),
	}
	overlappingExclusion := privatev1.ControlPlaneMaintenanceExclusion{
		Name:      "extended-freeze",
		StartTime: metav1.NewTime(time.Date(2026, time.October, 15, 1, 0, 0, 0, time.UTC)),
		EndTime:   metav1.NewTime(time.Date(2026, time.October, 15, 4, 0, 0, 0, time.UTC)),
	}
	clusterWith := func(w *privatev1.ControlPlaneMaintenanceWindow, exclusions ...privatev1.ControlPlaneMaintenanceExclusion) *privatev1.Cluster {
		c := &privatev1.Cluster{}
		if w != nil || len(exclusions) > 0 {
			var excl *[]privatev1.ControlPlaneMaintenanceExclusion
			if len(exclusions) > 0 {
				excl = &exclusions
			}
			c.Spec.Maintenance = &privatev1.ClusterMaintenanceSpec{
				Window:     w,
				Exclusions: excl,
			}
		}
		return c
	}
	tests := []struct {
		name       string
		cluster    *privatev1.Cluster
		now        time.Time
		permitted  bool
		reason     string
		nextWindow time.Time
	}{
		{name: "no window or exclusions", cluster: clusterWith(nil), now: time.Date(2026, time.October, 15, 1, 0, 0, 0, time.UTC), permitted: true},
		{name: "at window start", cluster: clusterWith(window), now: time.Date(2026, time.October, 14, 23, 0, 0, 0, time.UTC), permitted: true},
		{
			name: "before first configured occurrence", cluster: clusterWith(window), now: time.Date(2026, time.October, 7, 23, 30, 0, 0, time.UTC),
			reason: "OutsideMaintenanceWindow", nextWindow: window.StartTime.Time,
		},
		{
			name: "start date is not an allowed weekday", cluster: clusterWith(&privatev1.ControlPlaneMaintenanceWindow{
				StartTime: metav1.NewTime(time.Date(2026, time.October, 15, 23, 0, 0, 0, time.UTC)), DurationSeconds: 14400,
				Recurrence: window.Recurrence,
			}), now: time.Date(2026, time.October, 15, 22, 0, 0, 0, time.UTC),
			reason: "OutsideMaintenanceWindow", nextWindow: time.Date(2026, time.October, 21, 23, 0, 0, 0, time.UTC),
		},
		{name: "overnight window", cluster: clusterWith(window), now: time.Date(2026, time.October, 15, 1, 0, 0, 0, time.UTC), permitted: true},
		{
			name: "at window end", cluster: clusterWith(window), now: time.Date(2026, time.October, 15, 3, 0, 0, 0, time.UTC),
			reason: "OutsideMaintenanceWindow", nextWindow: time.Date(2026, time.October, 21, 23, 0, 0, 0, time.UTC),
		},
		{
			name: "exclusion within window", cluster: clusterWith(window, exclusion), now: time.Date(2026, time.October, 15, 1, 0, 0, 0, time.UTC),
			reason: "MaintenanceExclusionActive", nextWindow: exclusion.EndTime.Time,
		},
		{
			name: "exclusion without window", cluster: clusterWith(nil, exclusion), now: time.Date(2026, time.October, 15, 1, 0, 0, 0, time.UTC),
			reason: "MaintenanceExclusionActive", nextWindow: exclusion.EndTime.Time,
		},
		{
			name: "overlapping exclusion remains active", cluster: clusterWith(window, exclusion, overlappingExclusion),
			now:    time.Date(2026, time.October, 15, 2, 30, 0, 0, time.UTC),
			reason: "MaintenanceExclusionActive", nextWindow: overlappingExclusion.EndTime.Time,
		},
		{name: "exclusion ends", cluster: clusterWith(nil, exclusion), now: exclusion.EndTime.Time, permitted: true},
		{
			name: "unsupported frequency", cluster: clusterWith(&privatev1.ControlPlaneMaintenanceWindow{
				StartTime: window.StartTime, DurationSeconds: 14400,
				Recurrence: privatev1.ControlPlaneMaintenanceRecurrence{Frequency: "monthly", DaysOfWeek: []string{"wednesday"}},
			}), now: time.Date(2026, time.October, 15, 1, 0, 0, 0, time.UTC), reason: "InvalidMaintenanceWindow",
		},
		{
			name: "invalid day", cluster: clusterWith(&privatev1.ControlPlaneMaintenanceWindow{
				StartTime: window.StartTime, DurationSeconds: 14400,
				Recurrence: privatev1.ControlPlaneMaintenanceRecurrence{Frequency: "weekly", DaysOfWeek: []string{"WEDNESDAY"}},
			}), now: time.Date(2026, time.October, 15, 1, 0, 0, 0, time.UTC), reason: "InvalidMaintenanceWindow",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			permitted, reason, _, next := maintenancePermitted(tt.cluster, tt.now)
			require.Equal(t, tt.permitted, permitted)
			require.Equal(t, tt.reason, reason)
			if tt.nextWindow.IsZero() {
				require.Nil(t, next)
			} else {
				require.NotNil(t, next)
				require.Equal(t, tt.nextWindow, *next)
			}
		})
	}
}
