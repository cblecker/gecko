package v1

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
)

const (
	maxControlPlaneMaintenanceExclusions        = 3
	maxControlPlaneMaintenanceExclusionDuration = 30 * 24 * time.Hour
)

var validControlPlaneMaintenanceDays = map[string]struct{}{
	"monday":    {},
	"tuesday":   {},
	"wednesday": {},
	"thursday":  {},
	"friday":    {},
	"saturday":  {},
	"sunday":    {},
}

// Default sets private, system-derived defaults for Cluster.
func (c *Cluster) Default(_ context.Context) error {
	if c.Spec.SafeName == "" {
		c.Spec.SafeName = DefaultSafeName(c.Name, c.UID)
	}
	return nil
}

// ValidateCreate validates Cluster creation.
func (c *Cluster) ValidateCreate(_ context.Context) error {
	if c.Spec.SafeName != DefaultSafeName(c.Name, c.UID) {
		return fmt.Errorf("spec.safeName must be the default safe name for metadata.name")
	}
	return c.validateMaintenanceConfig()
}

// ValidateUpdate validates Cluster updates.
func (c *Cluster) ValidateUpdate(_ context.Context, oldObj runtime.Object) error {
	oldCluster, ok := oldObj.(*Cluster)
	if !ok {
		return fmt.Errorf("expected old object to be *Cluster, got %T", oldObj)
	}

	if oldCluster.Spec.SafeName == "" {
		if c.Spec.SafeName != DefaultSafeName(c.Name, c.UID) {
			return fmt.Errorf("spec.safeName must be the default safe name for metadata.name")
		}
		return c.validateMaintenanceConfig()
	}
	if c.Spec.SafeName != oldCluster.Spec.SafeName {
		return fmt.Errorf("spec.safeName is immutable")
	}
	return c.validateMaintenanceConfig()
}

// ValidateDelete validates Cluster deletion.
func (c *Cluster) ValidateDelete(_ context.Context) error {
	return nil
}

// validateMaintenanceConfig validates the inline maintenance settings.
func (c *Cluster) validateMaintenanceConfig() error {
	m := c.Spec.Maintenance
	if m == nil {
		return nil
	}
	var exclusions []ControlPlaneMaintenanceExclusion
	if m.Exclusions != nil {
		exclusions = *m.Exclusions
	}
	if len(exclusions) > maxControlPlaneMaintenanceExclusions {
		return fmt.Errorf("spec.maintenance.exclusions must contain at most 3 exclusions")
	}
	for i, exclusion := range exclusions {
		if exclusion.EndTime.Sub(exclusion.StartTime.Time) > maxControlPlaneMaintenanceExclusionDuration {
			return fmt.Errorf("spec.maintenance.exclusions[%d] must not exceed 30 days", i)
		}
	}

	if window := m.Window; window != nil {
		if window.DurationSeconds < 60 || window.DurationSeconds > 86400 {
			return fmt.Errorf("spec.maintenance.window.durationSeconds must be between 60 and 86400")
		}
		if window.Recurrence.Frequency != "weekly" {
			return fmt.Errorf("spec.maintenance.window.recurrence.frequency must be weekly")
		}
		if len(window.Recurrence.DaysOfWeek) == 0 {
			return fmt.Errorf("spec.maintenance.window.recurrence.daysOfWeek must contain at least one day")
		}

		seenDays := make(map[string]struct{}, len(window.Recurrence.DaysOfWeek))
		for _, day := range window.Recurrence.DaysOfWeek {
			if _, valid := validControlPlaneMaintenanceDays[day]; !valid {
				return fmt.Errorf("spec.maintenance.window.recurrence.daysOfWeek contains invalid day %q", day)
			}
			if _, duplicate := seenDays[day]; duplicate {
				return fmt.Errorf("spec.maintenance.window.recurrence.daysOfWeek contains duplicate day %q", day)
			}
			seenDays[day] = struct{}{}
		}
	}

	seenExclusions := make(map[string]struct{}, len(exclusions))
	for i, exclusion := range exclusions {
		if strings.TrimSpace(exclusion.Name) == "" {
			return fmt.Errorf("spec.maintenance.exclusions[%d].name must not be empty", i)
		}
		if _, duplicate := seenExclusions[exclusion.Name]; duplicate {
			return fmt.Errorf("spec.maintenance.exclusions contains duplicate name %q", exclusion.Name)
		}
		if !exclusion.EndTime.After(exclusion.StartTime.Time) {
			return fmt.Errorf("spec.maintenance.exclusions[%d].endTime must be after startTime", i)
		}
		seenExclusions[exclusion.Name] = struct{}{}
	}

	return validateMaintenanceAvailability(m.Window, exclusions)
}
