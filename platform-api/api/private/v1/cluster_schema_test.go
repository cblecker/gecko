package v1_test

import (
	"testing"
	"time"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/conversion"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
	publicv1 "github.com/openshift-online/gecko/platform-api/api/public/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// exclusionList is a helper that returns a pointer to a slice of exclusions,
// matching the *[]ControlPlaneMaintenanceExclusion type.
func exclusionList(e ...privatev1.ControlPlaneMaintenanceExclusion) *[]privatev1.ControlPlaneMaintenanceExclusion {
	s := e
	return &s
}

// clusterForConversionTest returns a minimal valid private Cluster with a
// maintenance window and one exclusion.
func clusterForConversionTest() privatev1.Cluster {
	return privatev1.Cluster{
		TypeMeta:   metav1.TypeMeta{APIVersion: privatev1.GroupVersion.String(), Kind: "Cluster"},
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-1", Namespace: "hyperfleet"},
		Spec: privatev1.ClusterSpec{
			Maintenance: &privatev1.ClusterMaintenanceSpec{
				Exclusions: exclusionList(privatev1.ControlPlaneMaintenanceExclusion{
					Name:      "holiday-freeze",
					StartTime: metav1.NewTime(time.Date(2026, time.December, 20, 0, 0, 0, 0, time.UTC)),
					EndTime:   metav1.NewTime(time.Date(2027, time.January, 3, 0, 0, 0, 0, time.UTC)),
				}),
			},
		},
	}
}

func newClusterConverter(t *testing.T) *conversion.Converter {
	t.Helper()
	privateScheme, publicScheme := runtime.NewScheme(), runtime.NewScheme()
	if err := privatev1.AddToScheme(privateScheme); err != nil {
		t.Fatal(err)
	}
	if err := publicv1.AddToScheme(publicScheme); err != nil {
		t.Fatal(err)
	}
	return conversion.NewConverter(publicScheme, privateScheme, "")
}

// TestClusterMaintenanceExclusionsClearedViaPublicAPI verifies that a public
// PUT that explicitly sends an empty exclusions list clears the stored exclusions.
func TestClusterMaintenanceExclusionsClearedViaPublicAPI(t *testing.T) {
	converter := newClusterConverter(t)
	existing := clusterForConversionTest()

	if existing.Spec.Maintenance.Exclusions == nil || len(*existing.Spec.Maintenance.Exclusions) != 1 {
		t.Fatalf("test setup: expected 1 exclusion")
	}

	// Simulate a public GET.
	pub, err := converter.PrivateToPublic(&existing)
	if err != nil {
		t.Fatalf("PrivateToPublic: %v", err)
	}
	publicCluster := pub.(*publicv1.Cluster)

	// Client explicitly clears the exclusion list by sending an empty pointer.
	empty := []publicv1.ControlPlaneMaintenanceExclusion{}
	publicCluster.Spec.Maintenance.Exclusions = &empty

	// Simulate a public PUT seeded from the existing object.
	updated, err := converter.PublicToPrivate(publicCluster, &existing)
	if err != nil {
		t.Fatalf("PublicToPrivate: %v", err)
	}
	updatedCluster := updated.(*privatev1.Cluster)

	excl := updatedCluster.Spec.Maintenance.Exclusions
	if excl == nil || len(*excl) != 0 {
		n := 0
		if excl != nil {
			n = len(*excl)
		}
		t.Fatalf("expected exclusions to be cleared (empty list), got %d exclusion(s)", n)
	}
}

// TestClusterMaintenanceExclusionsPreservedWhenExclusionsOmitted verifies that
// a public PUT that includes a maintenance object but omits exclusions preserves
// the existing exclusions (nil pointer = absent key in JSON = seed value kept).
func TestClusterMaintenanceExclusionsPreservedWhenExclusionsOmitted(t *testing.T) {
	converter := newClusterConverter(t)
	existing := clusterForConversionTest()
	existing.Spec.Maintenance.CriticalUpgrade = "4.15.1"

	// Public PUT with maintenance present but exclusions pointer nil (omitted).
	publicCluster := &publicv1.Cluster{
		TypeMeta:   metav1.TypeMeta{APIVersion: publicv1.GroupVersion.String(), Kind: "Cluster"},
		ObjectMeta: existing.ObjectMeta,
		Spec: publicv1.ClusterSpec{
			Maintenance: &publicv1.ClusterMaintenanceSpec{
				Exclusions: nil, // pointer nil = key absent from JSON
			},
		},
	}

	updated, err := converter.PublicToPrivate(publicCluster, &existing)
	if err != nil {
		t.Fatalf("PublicToPrivate: %v", err)
	}
	updatedCluster := updated.(*privatev1.Cluster)

	excl := updatedCluster.Spec.Maintenance.Exclusions
	if excl == nil || len(*excl) != 1 {
		t.Fatalf("expected exclusions to be preserved (1 entry)")
	}
	if updatedCluster.Spec.Maintenance.CriticalUpgrade != "4.15.1" {
		t.Fatalf("expected criticalUpgrade to be preserved, got %q", updatedCluster.Spec.Maintenance.CriticalUpgrade)
	}
}

// TestClusterMaintenanceExclusionsPreservedWhenMaintenanceOmitted verifies
// that omitting the entire maintenance field in a public PUT preserves the
// existing maintenance configuration.
func TestClusterMaintenanceExclusionsPreservedWhenMaintenanceOmitted(t *testing.T) {
	converter := newClusterConverter(t)
	existing := clusterForConversionTest()
	existing.Spec.Maintenance.CriticalUpgrade = "4.15.1"

	publicCluster := &publicv1.Cluster{
		TypeMeta:   metav1.TypeMeta{APIVersion: publicv1.GroupVersion.String(), Kind: "Cluster"},
		ObjectMeta: existing.ObjectMeta,
		Spec:       publicv1.ClusterSpec{Maintenance: nil},
	}

	updated, err := converter.PublicToPrivate(publicCluster, &existing)
	if err != nil {
		t.Fatalf("PublicToPrivate: %v", err)
	}
	updatedCluster := updated.(*privatev1.Cluster)

	excl := updatedCluster.Spec.Maintenance.Exclusions
	if excl == nil || len(*excl) != 1 {
		t.Fatalf("expected exclusions to be preserved (1 entry)")
	}
	if updatedCluster.Spec.Maintenance.CriticalUpgrade != "4.15.1" {
		t.Fatalf("expected criticalUpgrade to be preserved, got %q", updatedCluster.Spec.Maintenance.CriticalUpgrade)
	}
}

// TestClusterMaintenanceExclusionRoundTrips exercises the full lifecycle of
// maintenance exclusions through sequential public and private API operations.
// Each step builds on the result of the previous one, exercising setting,
// changing, and clearing exclusions via both API surfaces, and verifying that
// criticalUpgrade is preserved and not leaked across conversions at every step.
func TestClusterMaintenanceExclusionRoundTrips(t *testing.T) {
	converter := newClusterConverter(t)

	freeze := privatev1.ControlPlaneMaintenanceExclusion{
		Name:      "holiday-freeze",
		StartTime: metav1.NewTime(time.Date(2026, time.December, 20, 0, 0, 0, 0, time.UTC)),
		EndTime:   metav1.NewTime(time.Date(2027, time.January, 3, 0, 0, 0, 0, time.UTC)),
	}
	rollout := privatev1.ControlPlaneMaintenanceExclusion{
		Name:      "major-rollout",
		StartTime: metav1.NewTime(time.Date(2027, time.February, 1, 0, 0, 0, 0, time.UTC)),
		EndTime:   metav1.NewTime(time.Date(2027, time.February, 7, 0, 0, 0, 0, time.UTC)),
	}

	requireExclusions := func(t *testing.T, step string, stored *privatev1.Cluster, wantLen int, wantCritical string) {
		t.Helper()
		excl := stored.Spec.Maintenance.Exclusions
		if excl == nil {
			t.Fatalf("%s: Exclusions pointer is nil, want len=%d", step, wantLen)
		}
		if len(*excl) != wantLen {
			t.Fatalf("%s: got %d exclusion(s), want %d", step, len(*excl), wantLen)
		}
		if stored.Spec.Maintenance.CriticalUpgrade != wantCritical {
			t.Fatalf("%s: criticalUpgrade=%q, want %q", step, stored.Spec.Maintenance.CriticalUpgrade, wantCritical)
		}
	}

	publicPUT := func(t *testing.T, step string, converter *conversion.Converter, stored *privatev1.Cluster, mutate func(*publicv1.Cluster)) privatev1.Cluster {
		t.Helper()
		pub, err := converter.PrivateToPublic(stored)
		if err != nil {
			t.Fatalf("%s PrivateToPublic: %v", step, err)
		}
		pubCluster := pub.(*publicv1.Cluster)
		mutate(pubCluster)
		updated, err := converter.PublicToPrivate(pubCluster, stored)
		if err != nil {
			t.Fatalf("%s PublicToPrivate: %v", step, err)
		}
		return *updated.(*privatev1.Cluster)
	}

	// Step 0: cluster starts with no maintenance configured (fresh cluster).
	stored := privatev1.Cluster{
		TypeMeta:   metav1.TypeMeta{APIVersion: privatev1.GroupVersion.String(), Kind: "Cluster"},
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-1", Namespace: "hyperfleet"},
	}

	// Step 1: private API sets exclusion + criticalUpgrade (operator action).
	stored.Spec.Maintenance = &privatev1.ClusterMaintenanceSpec{
		Exclusions:      exclusionList(freeze),
		CriticalUpgrade: "4.15.1",
	}
	requireExclusions(t, "step1", &stored, 1, "4.15.1")

	// Step 2: public GET — exclusion visible, criticalUpgrade stripped.
	pub2, err := converter.PrivateToPublic(&stored)
	if err != nil {
		t.Fatalf("step2 PrivateToPublic: %v", err)
	}
	pub2Cluster := pub2.(*publicv1.Cluster)
	if pub2Cluster.Spec.Maintenance == nil {
		t.Fatal("step2: public Maintenance must not be nil")
	}
	excl2 := pub2Cluster.Spec.Maintenance.Exclusions
	if excl2 == nil || len(*excl2) != 1 || (*excl2)[0].Name != "holiday-freeze" {
		t.Fatalf("step2: expected 1 exclusion 'holiday-freeze' in public GET, got %v", excl2)
	}

	// Step 3: public PUT replacing the exclusion with a different one.
	stored = publicPUT(t, "step3", converter, &stored, func(c *publicv1.Cluster) {
		c.Spec.Maintenance.Exclusions = &[]publicv1.ControlPlaneMaintenanceExclusion{{
			Name:      rollout.Name,
			StartTime: metav1.NewTime(rollout.StartTime.Time),
			EndTime:   metav1.NewTime(rollout.EndTime.Time),
		}}
	})
	requireExclusions(t, "step3", &stored, 1, "4.15.1")
	if (*stored.Spec.Maintenance.Exclusions)[0].Name != "major-rollout" {
		t.Fatalf("step3: expected 'major-rollout', got %q", (*stored.Spec.Maintenance.Exclusions)[0].Name)
	}

	// Step 4: public PUT adding a second exclusion.
	stored = publicPUT(t, "step4", converter, &stored, func(c *publicv1.Cluster) {
		c.Spec.Maintenance.Exclusions = &[]publicv1.ControlPlaneMaintenanceExclusion{
			{Name: freeze.Name, StartTime: metav1.NewTime(freeze.StartTime.Time), EndTime: metav1.NewTime(freeze.EndTime.Time)},
			{Name: rollout.Name, StartTime: metav1.NewTime(rollout.StartTime.Time), EndTime: metav1.NewTime(rollout.EndTime.Time)},
		}
	})
	requireExclusions(t, "step4", &stored, 2, "4.15.1")

	// Step 5: public PUT with exclusions field omitted — must preserve both.
	stored = publicPUT(t, "step5", converter, &stored, func(c *publicv1.Cluster) {
		c.Spec.Maintenance.Exclusions = nil // client omits field
	})
	requireExclusions(t, "step5", &stored, 2, "4.15.1")

	// Step 6: public PUT explicitly clearing all exclusions.
	stored = publicPUT(t, "step6", converter, &stored, func(c *publicv1.Cluster) {
		empty := []publicv1.ControlPlaneMaintenanceExclusion{}
		c.Spec.Maintenance.Exclusions = &empty
	})
	requireExclusions(t, "step6", &stored, 0, "4.15.1")

	// Step 7: private API re-adds an exclusion after public clear.
	stored.Spec.Maintenance.Exclusions = exclusionList(rollout)
	requireExclusions(t, "step7", &stored, 1, "4.15.1")

	// Step 8: public GET final round-trip — no data loss.
	pub8, err := converter.PrivateToPublic(&stored)
	if err != nil {
		t.Fatalf("step8 PrivateToPublic: %v", err)
	}
	excl8 := pub8.(*publicv1.Cluster).Spec.Maintenance.Exclusions
	if excl8 == nil || len(*excl8) != 1 || (*excl8)[0].Name != "major-rollout" {
		t.Fatalf("step8: public GET must show 1 exclusion 'major-rollout', got %v", excl8)
	}
	// criticalUpgrade never appears in the public type (compile-time guarantee).
}

// TestClusterMaintenanceWindowClearedViaPublicAPI verifies the full set of
// window lifecycle behaviors through the public API:
//   - clearing by sending null
//   - preservation when maintenance is omitted entirely
//   - window is wiped when maintenance is present but window is omitted (PUT
//     replaces the full maintenance object — clients must re-send the window
//     if they only intend to change exclusions)
//   - round-trip: set → GET → PUT back → window unchanged
func TestClusterMaintenanceWindowClearedViaPublicAPI(t *testing.T) {
	converter := newClusterConverter(t)

	window := &privatev1.ControlPlaneMaintenanceWindow{
		StartTime:       metav1.NewTime(time.Date(2026, time.September, 26, 2, 0, 0, 0, time.UTC)),
		DurationSeconds: 14400,
		Recurrence: privatev1.ControlPlaneMaintenanceRecurrence{
			Frequency:  "weekly",
			DaysOfWeek: []string{"saturday"},
		},
	}
	newCluster := func() privatev1.Cluster {
		return privatev1.Cluster{
			TypeMeta:   metav1.TypeMeta{APIVersion: privatev1.GroupVersion.String(), Kind: "Cluster"},
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-1", Namespace: "hyperfleet"},
			Spec: privatev1.ClusterSpec{
				Maintenance: &privatev1.ClusterMaintenanceSpec{Window: window},
			},
		}
	}

	// Case 1: public PUT with window explicitly null — clears the window.
	t.Run("public_PUT_null_clears_window", func(t *testing.T) {
		existing := newCluster()
		pub, err := converter.PrivateToPublic(&existing)
		if err != nil {
			t.Fatalf("PrivateToPublic: %v", err)
		}
		pubCluster := pub.(*publicv1.Cluster)
		pubCluster.Spec.Maintenance.Window = nil

		updated, err := converter.PublicToPrivate(pubCluster, &existing)
		if err != nil {
			t.Fatalf("PublicToPrivate: %v", err)
		}
		if updated.(*privatev1.Cluster).Spec.Maintenance.Window != nil {
			t.Fatal("expected window to be cleared")
		}
	})

	// Case 2: public PUT with maintenance omitted entirely — preserves window.
	t.Run("public_PUT_maintenance_omitted_preserves_window", func(t *testing.T) {
		existing := newCluster()
		pubOmit := &publicv1.Cluster{
			TypeMeta:   metav1.TypeMeta{APIVersion: publicv1.GroupVersion.String(), Kind: "Cluster"},
			ObjectMeta: existing.ObjectMeta,
			Spec:       publicv1.ClusterSpec{Maintenance: nil},
		}
		updated, err := converter.PublicToPrivate(pubOmit, &existing)
		if err != nil {
			t.Fatalf("PublicToPrivate: %v", err)
		}
		if updated.(*privatev1.Cluster).Spec.Maintenance.Window == nil {
			t.Fatal("expected window to be preserved")
		}
	})

	// Case 3: public PUT with maintenance present but window absent (nil pointer)
	// clears the window. This is standard PUT semantics — the full maintenance
	// object is replaced. Clients updating only exclusions must re-send the window.
	t.Run("public_PUT_maintenance_present_window_absent_clears_window", func(t *testing.T) {
		existing := newCluster()
		pubNoWindow := &publicv1.Cluster{
			TypeMeta:   metav1.TypeMeta{APIVersion: publicv1.GroupVersion.String(), Kind: "Cluster"},
			ObjectMeta: existing.ObjectMeta,
			Spec: publicv1.ClusterSpec{
				Maintenance: &publicv1.ClusterMaintenanceSpec{
					Window: nil, // absent — serializes as "window":null
				},
			},
		}
		updated, err := converter.PublicToPrivate(pubNoWindow, &existing)
		if err != nil {
			t.Fatalf("PublicToPrivate: %v", err)
		}
		if updated.(*privatev1.Cluster).Spec.Maintenance.Window != nil {
			t.Fatal("expected window to be cleared when maintenance is present but window is null")
		}
	})

	// Case 4: GET → PUT round-trip preserves the window unchanged.
	t.Run("public_GET_PUT_roundtrip_preserves_window", func(t *testing.T) {
		existing := newCluster()
		pub, err := converter.PrivateToPublic(&existing)
		if err != nil {
			t.Fatalf("PrivateToPublic: %v", err)
		}
		// Client sends the object back as-is (no modifications).
		updated, err := converter.PublicToPrivate(pub, &existing)
		if err != nil {
			t.Fatalf("PublicToPrivate: %v", err)
		}
		got := updated.(*privatev1.Cluster).Spec.Maintenance.Window
		if got == nil {
			t.Fatal("expected window to survive round-trip unchanged")
		}
		if got.DurationSeconds != window.DurationSeconds {
			t.Fatalf("window durationSeconds changed: got %d, want %d", got.DurationSeconds, window.DurationSeconds)
		}
	})
}

// TestClusterMaintenanceCriticalUpgradeNotExposedViaPublicAPI verifies that
// criticalUpgrade is stripped on public GET and not overwritable by a public PUT.
func TestClusterMaintenanceCriticalUpgradeNotExposedViaPublicAPI(t *testing.T) {
	converter := newClusterConverter(t)
	existing := clusterForConversionTest()
	existing.Spec.Maintenance.CriticalUpgrade = "4.15.1"

	pub, err := converter.PrivateToPublic(&existing)
	if err != nil {
		t.Fatalf("PrivateToPublic: %v", err)
	}
	publicCluster := pub.(*publicv1.Cluster)
	if publicCluster.Spec.Maintenance == nil {
		t.Fatal("expected public Maintenance to be non-nil")
	}

	updated, err := converter.PublicToPrivate(publicCluster, &existing)
	if err != nil {
		t.Fatalf("PublicToPrivate: %v", err)
	}
	updatedCluster := updated.(*privatev1.Cluster)
	if updatedCluster.Spec.Maintenance.CriticalUpgrade != "4.15.1" {
		t.Fatalf("criticalUpgrade was overwritten: got %q, want %q",
			updatedCluster.Spec.Maintenance.CriticalUpgrade, "4.15.1")
	}
}
