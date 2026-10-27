package v1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// validClusterWithWindow builds a Cluster with a valid maintenance window and one exclusion.
func validClusterWithWindow() *Cluster {
	c := &Cluster{}
	c.SetName("cluster-1")
	c.SetUID(types.UID("550e8400-e29b-41d4-a716-446655440000"))
	c.Spec.SafeName = DefaultSafeName(c.Name, c.UID)

	exclusionStart := metav1.NewTime(time.Date(2026, time.December, 20, 0, 0, 0, 0, time.UTC))
	exclusionEnd := metav1.NewTime(time.Date(2027, time.January, 3, 0, 0, 0, 0, time.UTC))

	exclusions := []ControlPlaneMaintenanceExclusion{
		{Name: "holiday-freeze", StartTime: exclusionStart, EndTime: exclusionEnd},
	}
	c.Spec.Maintenance = &ClusterMaintenanceSpec{
		Window: &ControlPlaneMaintenanceWindow{
			StartTime:       metav1.NewTime(time.Date(2026, time.September, 26, 2, 0, 0, 0, time.UTC)),
			DurationSeconds: 14400, // 4 hours
			Recurrence: ControlPlaneMaintenanceRecurrence{
				Frequency:  "weekly",
				DaysOfWeek: []string{"saturday"},
			},
		},
		Exclusions: &exclusions,
	}
	return c
}

func TestClusterValidateCreate_ValidMaintenanceConfig(t *testing.T) {
	if err := validClusterWithWindow().ValidateCreate(t.Context()); err != nil {
		t.Fatalf("ValidateCreate() error = %v", err)
	}
}

func TestClusterValidateCreate_InvalidMaintenanceConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Cluster)
	}{
		{
			name: "duration below minimum",
			mutate: func(c *Cluster) {
				c.Spec.Maintenance.Window.DurationSeconds = 59
			},
		},
		{
			name: "duration exceeds 24 hours",
			mutate: func(c *Cluster) {
				c.Spec.Maintenance.Window.DurationSeconds = 86401
			},
		},
		{
			name: "unsupported frequency",
			mutate: func(c *Cluster) {
				c.Spec.Maintenance.Window.Recurrence.Frequency = "daily"
			},
		},
		{
			name: "missing weekday",
			mutate: func(c *Cluster) {
				c.Spec.Maintenance.Window.Recurrence.DaysOfWeek = nil
			},
		},
		{
			name: "invalid weekday",
			mutate: func(c *Cluster) {
				c.Spec.Maintenance.Window.Recurrence.DaysOfWeek = []string{"weekend"}
			},
		},
		{
			name: "weekday must be lowercase",
			mutate: func(c *Cluster) {
				c.Spec.Maintenance.Window.Recurrence.DaysOfWeek = []string{"Saturday"}
			},
		},
		{
			name: "duplicate weekday",
			mutate: func(c *Cluster) {
				c.Spec.Maintenance.Window.Recurrence.DaysOfWeek = []string{"saturday", "saturday"}
			},
		},
		{
			name: "empty exclusion name",
			mutate: func(c *Cluster) {
				(*c.Spec.Maintenance.Exclusions)[0].Name = " "
			},
		},
		{
			name: "duplicate exclusion name",
			mutate: func(c *Cluster) {
				*c.Spec.Maintenance.Exclusions = append(
					*c.Spec.Maintenance.Exclusions,
					(*c.Spec.Maintenance.Exclusions)[0],
				)
			},
		},
		{
			name: "exclusion endTime not after startTime",
			mutate: func(c *Cluster) {
				(*c.Spec.Maintenance.Exclusions)[0].EndTime = (*c.Spec.Maintenance.Exclusions)[0].StartTime
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validClusterWithWindow()
			tt.mutate(c)
			if err := c.ValidateCreate(t.Context()); err == nil {
				t.Fatal("expected maintenance config validation error")
			}
		})
	}
}

func TestClusterValidateCreate_MaximumDuration(t *testing.T) {
	c := validClusterWithWindow()
	c.Spec.Maintenance.Window.DurationSeconds = 86400 // 24 hours
	if err := c.ValidateCreate(t.Context()); err != nil {
		t.Fatalf("ValidateCreate() error = %v", err)
	}
}

func TestClusterValidateCreate_NoMaintenanceWindow(t *testing.T) {
	c := validClusterWithWindow()
	c.Spec.Maintenance = nil
	if err := c.ValidateCreate(t.Context()); err != nil {
		t.Fatalf("ValidateCreate() error = %v", err)
	}
}

func TestClusterValidateUpdate_MaintenanceConfigValid(t *testing.T) {
	old := validClusterWithWindow()
	updated := old.DeepCopy()
	updated.Spec.Maintenance.Window.DurationSeconds = 7200 // 2 hours

	if err := updated.ValidateUpdate(t.Context(), old); err != nil {
		t.Fatalf("ValidateUpdate() error = %v", err)
	}
}

func TestClusterValidateUpdate_InvalidMaintenanceConfig(t *testing.T) {
	old := validClusterWithWindow()
	updated := old.DeepCopy()
	updated.Spec.Maintenance.Window.Recurrence.Frequency = "monthly"

	if err := updated.ValidateUpdate(t.Context(), old); err == nil {
		t.Fatal("expected maintenance config validation error on update")
	}
}
