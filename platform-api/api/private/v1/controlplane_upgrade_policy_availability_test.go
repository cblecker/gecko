package v1

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Integer-hour schedules have all availability boundaries on the hour, so an
// exhaustive hourly model independently checks every possible rolling minimum.
func TestControlPlaneUpgradePolicyAvailabilityAgainstHourlyModel(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 32))
	start := time.Date(2026, time.January, 5, 2, 0, 0, 0, time.UTC)
	weekdays := []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
	for trial := range 256 {
		policy := validControlPlaneUpgradePolicy()
		window := policy.Spec.MaintenanceWindow
		window.Start = metav1.NewTime(start)
		hours := 1 + rng.IntN(24)
		window.DurationMinutes = int32(hours * 60)
		window.Recurrence.DaysOfWeek = nil
		selected := make(map[string]bool)
		for _, weekday := range weekdays {
			if rng.IntN(2) == 0 {
				window.Recurrence.DaysOfWeek = append(window.Recurrence.DaysOfWeek, weekday)
				selected[weekday] = true
			}
		}
		if len(selected) == 0 {
			window.Recurrence.DaysOfWeek = []string{"monday"}
			selected["monday"] = true
		}
		policy.Spec.MaintenanceExclusions = nil
		latestStart := 7 * 24
		var blackouts [][2]int
		for i := range rng.IntN(4) {
			from := rng.IntN(150*24) - 10*24
			to := from + 1 + rng.IntN(30*24)
			blackouts = append(blackouts, [2]int{from, to})
			policy.Spec.MaintenanceExclusions = append(policy.Spec.MaintenanceExclusions,
				maintenanceTestExclusion(fmt.Sprintf("freeze-%d", i), start.Add(time.Duration(from)*time.Hour), start.Add(time.Duration(to)*time.Hour)))
			latestStart = max(latestStart, to+7*24)
		}
		const period = 32 * 24
		prefix := make([]int, latestStart+period+1)
		for hour := 0; hour < len(prefix)-1; hour++ {
			permitted := hour%24 < hours && selected[strings.ToLower(start.AddDate(0, 0, hour/24).Weekday().String())]
			for _, blackout := range blackouts {
				if hour >= blackout[0] && hour < blackout[1] {
					permitted = false
				}
			}
			prefix[hour+1] = prefix[hour]
			if permitted {
				prefix[hour+1]++
			}
		}
		invalid := false
		for hour := 0; hour <= latestStart; hour++ {
			if prefix[hour+period]-prefix[hour] < 48 {
				invalid = true
				break
			}
		}
		if err := policy.ValidateCreate(t.Context()); (err != nil) != invalid {
			t.Fatalf("trial %d: error=%v, model invalid=%v, hours=%d, days=%v, blackouts=%v", trial, err, invalid, hours, window.Recurrence.DaysOfWeek, blackouts)
		}
	}
}

func TestControlPlaneUpgradePolicyExclusionLimits(t *testing.T) {
	start := time.Date(2026, time.January, 5, 2, 0, 0, 0, time.UTC)
	const day = 24 * time.Hour
	exclusion := func(name string, duration time.Duration) ControlPlaneMaintenanceExclusion {
		return maintenanceTestExclusion(name, start, start.Add(duration))
	}
	for _, tc := range []struct {
		name       string
		exclusions []ControlPlaneMaintenanceExclusion
		wantError  string
	}{
		{name: "no exclusions"},
		{
			name: "three exclusions each exactly thirty days",
			exclusions: []ControlPlaneMaintenanceExclusion{
				exclusion("one", 30*day), exclusion("two", 30*day), exclusion("three", 30*day),
			},
		},
		{
			name: "four exclusions rejected before other exclusion validation",
			exclusions: []ControlPlaneMaintenanceExclusion{
				{}, {}, {}, {},
			},
			wantError: "at most 3",
		},
		{
			name:       "thirty days and one nanosecond",
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("too-long", 30*day+time.Nanosecond)},
			wantError:  "30 days",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := validControlPlaneUpgradePolicy()
			policy.Spec.MaintenanceWindow = nil
			policy.Spec.MaintenanceExclusions = tc.exclusions
			assertMaintenancePolicyValidation(t, policy, tc.wantError)
		})
	}
}

func TestControlPlaneUpgradePolicyRollingAvailabilityWithoutWindow(t *testing.T) {
	start := time.Date(2026, time.January, 5, 2, 0, 0, 0, time.UTC)
	const day = 24 * time.Hour
	exclusion := func(name string, from, to time.Duration) ControlPlaneMaintenanceExclusion {
		return maintenanceTestExclusion(name, start.Add(from), start.Add(to))
	}
	for _, tc := range []struct {
		name       string
		exclusions []ControlPlaneMaintenanceExclusion
		wantError  string
	}{
		{name: "no exclusions"},
		{
			name:       "thirty day freeze leaves exactly forty eight hours",
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 0, 30*day)},
		},
		{
			name: "thirty days plus four and five hours leaves thirty nine hours",
			exclusions: []ControlPlaneMaintenanceExclusion{
				exclusion("freeze", 0, 30*day),
				exclusion("four-hours", 30*day, 30*day+4*time.Hour),
				exclusion("five-hours", 30*day+4*time.Hour, 30*day+9*time.Hour),
			},
			wantError: "at least 48 hours",
		},
		{
			name: "one nanosecond below forty eight hours",
			exclusions: []ControlPlaneMaintenanceExclusion{
				exclusion("freeze", 0, 30*day), exclusion("extra", 30*day, 30*day+time.Nanosecond),
			},
			wantError: "at least 48 hours",
		},
		{
			name: "overlapping freezes count once",
			exclusions: []ControlPlaneMaintenanceExclusion{
				exclusion("one", 0, 30*day), exclusion("two", 0, 30*day), exclusion("three", 0, 30*day),
			},
		},
		{
			name: "separated freezes leave exactly forty eight hours",
			exclusions: []ControlPlaneMaintenanceExclusion{
				exclusion("one", 0, 15*day), exclusion("two", 16*day, 31*day),
			},
		},
		{
			name: "separated freezes leave only twenty four hours",
			exclusions: []ControlPlaneMaintenanceExclusion{
				exclusion("one", 0, 15*day), exclusion("two", 16*day, 32*day),
			},
			wantError: "at least 48 hours",
		},
		{
			name: "distant freezes do not require scanning intervening years",
			exclusions: []ControlPlaneMaintenanceExclusion{
				maintenanceTestExclusion("later", time.Date(9000, time.January, 1, 2, 0, 0, 0, time.UTC), time.Date(9000, time.January, 31, 2, 0, 0, 0, time.UTC)),
				exclusion("earlier", 0, 30*day),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := validControlPlaneUpgradePolicy()
			policy.Spec.MaintenanceWindow = nil
			policy.Spec.MaintenanceExclusions = tc.exclusions
			assertMaintenancePolicyValidation(t, policy, tc.wantError)
		})
	}
}

func TestControlPlaneUpgradePolicyRollingAvailability(t *testing.T) {
	start := time.Date(2026, time.January, 5, 2, 0, 0, 0, time.UTC)
	const day = 24 * time.Hour
	allDays := []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
	exclusion := func(name string, from, to time.Duration) ControlPlaneMaintenanceExclusion {
		return maintenanceTestExclusion(name, start.Add(from), start.Add(to))
	}
	for _, tc := range []struct {
		name       string
		start      time.Time
		duration   int32
		days       []string
		exclusions []ControlPlaneMaintenanceExclusion
		invalid    bool
	}{
		{name: "daily four hour window", duration: 240, days: allDays},
		{name: "weekly twelve hours leaves exactly forty eight", duration: 720, days: []string{"monday"}},
		{name: "weekly one minute below minimum", duration: 719, days: []string{"monday"}, invalid: true},
		{name: "weekly four hours cannot provide forty eight", duration: 240, days: []string{"monday"}, invalid: true},
		{name: "activation is not a selected weekday", start: start.Add(day), duration: 720, days: []string{"monday"}},
		{
			name: "twenty day freeze leaves exactly forty eight daily hours", duration: 240, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 100*day, 120*day)},
		},
		{
			name: "one second below forty eight hours", duration: 240, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 100*day, 120*day+time.Second)}, invalid: true,
		},
		{
			name: "one nanosecond below forty eight hours", duration: 240, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 100*day, 120*day+time.Nanosecond)}, invalid: true,
		},
		{
			name: "identical overlapping freezes counted once", duration: 1440, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("one", 100*day, 130*day), exclusion("two", 100*day, 130*day)},
		},
		{
			name: "overlap union exceeds thirty days", duration: 1440, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("one", 100*day, 130*day), exclusion("two", 101*day, 131*day)}, invalid: true,
		},
		{
			name: "adjacent freezes leave exactly forty eight hours", duration: 1440, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("one", 100*day, 115*day), exclusion("two", 115*day, 130*day)},
		},
		{
			name: "separated freezes leave exactly forty eight hours", duration: 1440, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("one", 100*day, 115*day), exclusion("two", 116*day, 131*day)},
		},
		{
			name: "separated freezes remove thirty one days", duration: 1440, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("one", 100*day, 115*day), exclusion("two", 116*day, 132*day)}, invalid: true,
		},
		{
			name: "rolling violation between aligned thirty two day periods", duration: 1440, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("one", 17*day, 32*day), exclusion("two", 32*day, 48*day)}, invalid: true,
		},
		{
			name: "freeze outside weekly maintenance time", duration: 720, days: []string{"monday"},
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", day, 5*day)},
		},
		{
			name: "freeze ends exactly when window opens", duration: 720, days: []string{"monday"},
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 105*day-time.Second, 105*day)},
		},
		{
			name: "freeze starts exactly when window closes", duration: 720, days: []string{"monday"},
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 105*day+12*time.Hour, 106*day)},
		},
		{
			name: "overnight windows", start: start.Add(20 * time.Hour), duration: 240, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 100*day+20*time.Hour, 120*day+20*time.Hour)},
		},
		{
			name: "timestamps normalized to UTC", start: start.In(time.FixedZone("offset", 5*60*60+30*60)), duration: 240, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 100*day, 120*day)},
		},
		{
			name: "leap day and year boundaries", duration: 1440, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{
				maintenanceTestExclusion("leap", time.Date(2028, time.February, 20, 2, 0, 0, 0, time.UTC), time.Date(2028, time.March, 21, 2, 0, 0, 0, time.UTC)),
				maintenanceTestExclusion("year", time.Date(2028, time.December, 20, 2, 0, 0, 0, time.UTC), time.Date(2029, time.January, 19, 2, 0, 0, 0, time.UTC)),
			},
		},
		{
			name: "distant exclusions do not require scanning intervening years", duration: 240, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{
				maintenanceTestExclusion("distant", time.Date(4000, time.January, 1, 2, 0, 0, 0, time.UTC), time.Date(4000, time.January, 21, 2, 0, 0, 0, time.UTC)),
				maintenanceTestExclusion("farther", time.Date(9000, time.January, 1, 2, 0, 0, 0, time.UTC), time.Date(9000, time.January, 21, 2, 0, 0, 0, time.UTC)),
			},
		},
		{
			name: "exclusions before activation do not reduce availability", duration: 720, days: []string{"monday"},
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("past", -30*day, 0)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := validControlPlaneUpgradePolicy()
			policy.Spec.MaintenanceExclusions = tc.exclusions
			policy.Spec.MaintenanceWindow.Start = metav1.NewTime(start)
			if !tc.start.IsZero() {
				policy.Spec.MaintenanceWindow.Start = metav1.NewTime(tc.start)
			}
			policy.Spec.MaintenanceWindow.DurationMinutes = tc.duration
			policy.Spec.MaintenanceWindow.Recurrence.DaysOfWeek = tc.days
			wantError := ""
			if tc.invalid {
				wantError = "at least 48 hours"
			}
			assertMaintenancePolicyValidation(t, policy, wantError)
		})
	}
}

func TestControlPlaneUpgradePolicyAvailabilityUpdates(t *testing.T) {
	const day = 24 * time.Hour
	for _, tc := range []struct {
		name      string
		mutate    func(*ControlPlaneUpgradePolicy)
		wantError string
	}{
		{name: "remove exclusions", mutate: func(p *ControlPlaneUpgradePolicy) { p.Spec.MaintenanceExclusions = nil }},
		{name: "remove window", mutate: func(p *ControlPlaneUpgradePolicy) { p.Spec.MaintenanceWindow = nil }},
		{
			name: "shorten exclusion",
			mutate: func(p *ControlPlaneUpgradePolicy) {
				p.Spec.MaintenanceExclusions[0].End.Time = p.Spec.MaintenanceExclusions[0].End.Add(-day)
			},
		},
		{
			name: "add exclusion outside maintenance hours",
			mutate: func(p *ControlPlaneUpgradePolicy) {
				start := p.Spec.MaintenanceExclusions[0].End.Add(6 * time.Hour)
				p.Spec.MaintenanceExclusions = append(p.Spec.MaintenanceExclusions, maintenanceTestExclusion("extra", start, start.Add(time.Hour)))
			},
		},
		{
			name: "extend exclusion across availability boundary",
			mutate: func(p *ControlPlaneUpgradePolicy) {
				p.Spec.MaintenanceExclusions[0].End.Time = p.Spec.MaintenanceExclusions[0].End.Add(time.Second)
			},
			wantError: "at least 48 hours",
		},
		{
			name: "add exclusion across availability boundary",
			mutate: func(p *ControlPlaneUpgradePolicy) {
				start := p.Spec.MaintenanceExclusions[0].End.Time
				p.Spec.MaintenanceExclusions = append(p.Spec.MaintenanceExclusions, maintenanceTestExclusion("extra", start, start.Add(time.Second)))
			},
			wantError: "at least 48 hours",
		},
		{
			name:      "shorten maintenance window",
			mutate:    func(p *ControlPlaneUpgradePolicy) { p.Spec.MaintenanceWindow.DurationMinutes-- },
			wantError: "at least 48 hours",
		},
		{
			name: "remove a maintenance weekday",
			mutate: func(p *ControlPlaneUpgradePolicy) {
				p.Spec.MaintenanceWindow.Recurrence.DaysOfWeek = p.Spec.MaintenanceWindow.Recurrence.DaysOfWeek[:6]
			},
			wantError: "at least 48 hours",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := validControlPlaneUpgradePolicy()
			start := old.Spec.MaintenanceWindow.Start.Add(100 * day)
			old.Spec.MaintenanceExclusions = []ControlPlaneMaintenanceExclusion{maintenanceTestExclusion("freeze", start, start.Add(20*day))}
			updated := old.DeepCopy()
			tc.mutate(updated)
			err := updated.ValidateUpdate(t.Context(), old)
			assertMaintenanceValidationError(t, err, tc.wantError)
		})
	}
	t.Run("adding window revalidates existing exclusions", func(t *testing.T) {
		old := validControlPlaneUpgradePolicy()
		start := old.Spec.MaintenanceWindow.Start.Time
		old.Spec.MaintenanceWindow = nil
		old.Spec.MaintenanceExclusions = []ControlPlaneMaintenanceExclusion{maintenanceTestExclusion("freeze", start, start.Add(30*day))}
		if err := old.ValidateCreate(t.Context()); err != nil {
			t.Fatal(err)
		}
		updated := old.DeepCopy()
		updated.Spec.MaintenanceWindow = validControlPlaneUpgradePolicy().Spec.MaintenanceWindow
		assertMaintenanceValidationError(t, updated.ValidateUpdate(t.Context(), old), "at least 48 hours")
	})
}

func maintenanceTestExclusion(name string, start, end time.Time) ControlPlaneMaintenanceExclusion {
	return ControlPlaneMaintenanceExclusion{Name: name, Start: metav1.NewTime(start), End: metav1.NewTime(end)}
}

func assertMaintenancePolicyValidation(t *testing.T, policy *ControlPlaneUpgradePolicy, wantError string) {
	t.Helper()
	assertMaintenanceValidationError(t, policy.ValidateCreate(t.Context()), wantError)
	old := validControlPlaneUpgradePolicy()
	assertMaintenanceValidationError(t, policy.ValidateUpdate(t.Context(), old), wantError)
}

func assertMaintenanceValidationError(t *testing.T, err error, wantError string) {
	t.Helper()
	if wantError == "" {
		if err != nil {
			t.Fatalf("unexpected validation error: %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("validation error = %v, want message containing %q", err, wantError)
	}
}
