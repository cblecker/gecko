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
// exhaustive hourly model independently checks every possible rolling minimum,
// including a variable wait before the schedule activates.
func TestClusterMaintenanceAvailabilityAgainstHourlyModel(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 32))
	start := time.Date(2026, time.January, 5, 2, 0, 0, 0, time.UTC)
	weekdays := []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
	for trial := range 256 {
		cluster := validClusterWithWindow()
		window := cluster.Spec.Maintenance.Window
		window.StartTime = metav1.NewTime(start)
		hours := 1 + rng.IntN(24)
		window.DurationSeconds = int32(hours * 60 * 60)
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
		exclusions := []ControlPlaneMaintenanceExclusion{}
		cluster.Spec.Maintenance.Exclusions = &exclusions
		latestStart := 7 * 24
		var blackouts [][2]int
		for i := range rng.IntN(4) {
			from := rng.IntN(150*24) - 10*24
			to := from + 1 + rng.IntN(30*24)
			blackouts = append(blackouts, [2]int{from, to})
			exclusions = append(exclusions,
				maintenanceTestExclusion(fmt.Sprintf("freeze-%d", i), start.Add(time.Duration(from)*time.Hour), start.Add(time.Duration(to)*time.Hour)))
			latestStart = max(latestStart, to+7*24)
		}
		const period = 32 * 24
		firstHour := -rng.IntN(41 * 24)
		prefix := make([]int, latestStart-firstHour+period+1)
		for offset := 0; offset < len(prefix)-1; offset++ {
			hour := firstHour + offset
			permitted := hour >= 0 && hour%24 < hours && selected[strings.ToLower(start.AddDate(0, 0, hour/24).Weekday().String())]
			for _, blackout := range blackouts {
				if hour >= blackout[0] && hour < blackout[1] {
					permitted = false
				}
			}
			prefix[offset+1] = prefix[offset]
			if permitted {
				prefix[offset+1]++
			}
		}
		invalid := false
		for offset := 0; offset <= latestStart-firstHour; offset++ {
			if prefix[offset+period]-prefix[offset] < 48 {
				invalid = true
				break
			}
		}
		if err := validateMaintenanceAvailability(window, exclusions, start.Add(time.Duration(firstHour)*time.Hour)); (err != nil) != invalid {
			t.Fatalf("trial %d: error=%v, model invalid=%v, hours=%d, days=%v, blackouts=%v", trial, err, invalid, hours, window.Recurrence.DaysOfWeek, blackouts)
		}
	}
}

func TestClusterMaintenanceFutureStart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start time.Time
	}{
		{name: "one year away", start: time.Now().UTC().AddDate(1, 0, 0)},
		{name: "year 9999", start: time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := validClusterWithWindow()
			cluster.Spec.Maintenance.Exclusions = nil
			cluster.Spec.Maintenance.Window.StartTime = metav1.NewTime(tc.start)
			cluster.Spec.Maintenance.Window.DurationSeconds = 86400
			t.Run("create", func(t *testing.T) {
				assertMaintenanceValidationError(t, cluster.ValidateCreate(t.Context()), "at least 48 hours")
			})
			t.Run("update", func(t *testing.T) {
				assertMaintenanceValidationError(t, cluster.ValidateUpdate(t.Context(), validClusterWithWindow()), "at least 48 hours")
			})
		})
	}
}

func TestClusterMaintenanceInitialAvailability(t *testing.T) {
	now := time.Date(2026, time.January, 5, 2, 0, 0, 0, time.UTC)
	const day = 24 * time.Hour
	for _, tc := range []struct {
		name       string
		delay      time.Duration
		duration   int32
		days       []string
		noWindow   bool
		exclusions []ControlPlaneMaintenanceExclusion
		wantError  string
	}{
		{name: "already active", delay: -day, duration: 86400},
		{name: "starts now", duration: 86400},
		{name: "thirty day wait leaves exactly forty eight hours", delay: 30 * day, duration: 86400},
		{name: "one nanosecond too late", delay: 30*day + time.Nanosecond, duration: 86400, wantError: "at least 48 hours"},
		{name: "weekly schedule has enough initial occurrences", delay: 7 * day, duration: 43200, days: []string{"monday"}},
		{name: "weekly schedule misses the first occurrence", delay: 8 * day, duration: 43200, days: []string{"monday"}, wantError: "at least 48 hours"},
		{
			name: "initial wait and freeze leave exactly forty eight hours", delay: 20 * day, duration: 86400,
			exclusions: []ControlPlaneMaintenanceExclusion{maintenanceTestExclusion("freeze", now.Add(20*day), now.Add(30*day))},
		},
		{
			name: "initial wait and freeze together violate availability", delay: 20 * day, duration: 86400,
			exclusions: []ControlPlaneMaintenanceExclusion{maintenanceTestExclusion("freeze", now.Add(20*day), now.Add(31*day))},
			wantError:  "at least 48 hours",
		},
		{
			name: "no window remains available before distant exclusions", noWindow: true,
			exclusions: []ControlPlaneMaintenanceExclusion{maintenanceTestExclusion("distant",
				time.Date(9000, time.January, 1, 0, 0, 0, 0, time.UTC), time.Date(9000, time.January, 31, 0, 0, 0, 0, time.UTC))},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			window := validClusterWithWindow().Spec.Maintenance.Window
			window.StartTime = metav1.NewTime(now.Add(tc.delay))
			window.DurationSeconds = tc.duration
			if tc.days != nil {
				window.Recurrence.DaysOfWeek = tc.days
			}
			if tc.noWindow {
				window = nil
			}
			assertMaintenanceValidationError(t, validateMaintenanceAvailability(window, tc.exclusions, now), tc.wantError)
		})
	}
}

func TestClusterMaintenanceExclusionLimits(t *testing.T) {
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
			cluster := validClusterWithWindow()
			cluster.Spec.Maintenance.Window = nil
			cluster.Spec.Maintenance.Exclusions = &tc.exclusions
			assertMaintenanceClusterValidation(t, cluster, tc.wantError)
		})
	}
}

func TestClusterMaintenanceRollingAvailabilityWithoutWindow(t *testing.T) {
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
			cluster := validClusterWithWindow()
			cluster.Spec.Maintenance.Window = nil
			cluster.Spec.Maintenance.Exclusions = &tc.exclusions
			assertMaintenanceClusterValidation(t, cluster, tc.wantError)
		})
	}
}

func TestClusterMaintenanceAvailabilityWithoutExclusions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		exclusions *[]ControlPlaneMaintenanceExclusion
	}{
		{name: "omitted exclusions"},
		{name: "empty exclusions", exclusions: &[]ControlPlaneMaintenanceExclusion{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := validClusterWithWindow()
			cluster.Spec.Maintenance.Exclusions = tc.exclusions
			cluster.Spec.Maintenance.Window.Recurrence.DaysOfWeek = []string{"monday"}
			cluster.Spec.Maintenance.Window.DurationSeconds = 43200
			assertMaintenanceClusterValidation(t, cluster, "")
			cluster.Spec.Maintenance.Window.DurationSeconds--
			assertMaintenanceClusterValidation(t, cluster, "at least 48 hours")
		})
	}
}

func TestClusterMaintenanceRollingAvailability(t *testing.T) {
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
		{name: "daily four hour window", duration: 14400, days: allDays},
		{name: "weekly twelve hours leaves exactly forty eight", duration: 43200, days: []string{"monday"}},
		{name: "weekly one minute below minimum", duration: 43140, days: []string{"monday"}, invalid: true},
		{name: "weekly one second below minimum", duration: 43199, days: []string{"monday"}, invalid: true},
		{name: "weekly four hours cannot provide forty eight", duration: 14400, days: []string{"monday"}, invalid: true},
		{name: "activation is not a selected weekday", start: start.Add(day), duration: 43200, days: []string{"monday"}},
		{
			name: "twenty day freeze leaves exactly forty eight daily hours", duration: 14400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 100*day, 120*day)},
		},
		{
			name: "one second below forty eight hours", duration: 14400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 100*day, 120*day+time.Second)}, invalid: true,
		},
		{
			name: "one nanosecond below forty eight hours", duration: 14400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 100*day, 120*day+time.Nanosecond)}, invalid: true,
		},
		{
			name: "identical overlapping freezes counted once", duration: 86400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("one", 100*day, 130*day), exclusion("two", 100*day, 130*day)},
		},
		{
			name: "overlap union exceeds thirty days", duration: 86400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("one", 100*day, 130*day), exclusion("two", 101*day, 131*day)}, invalid: true,
		},
		{
			name: "adjacent freezes leave exactly forty eight hours", duration: 86400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("one", 100*day, 115*day), exclusion("two", 115*day, 130*day)},
		},
		{
			name: "separated freezes leave exactly forty eight hours", duration: 86400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("one", 100*day, 115*day), exclusion("two", 116*day, 131*day)},
		},
		{
			name: "separated freezes remove thirty one days", duration: 86400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("one", 100*day, 115*day), exclusion("two", 116*day, 132*day)}, invalid: true,
		},
		{
			name: "rolling violation between aligned thirty two day periods", duration: 86400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("one", 17*day, 32*day), exclusion("two", 32*day, 48*day)}, invalid: true,
		},
		{
			name: "freeze outside weekly maintenance time", duration: 43200, days: []string{"monday"},
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", day, 5*day)},
		},
		{
			name: "freeze ends exactly when window opens", duration: 43200, days: []string{"monday"},
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 105*day-time.Second, 105*day)},
		},
		{
			name: "freeze starts exactly when window closes", duration: 43200, days: []string{"monday"},
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 105*day+12*time.Hour, 106*day)},
		},
		{
			name: "overnight windows", start: start.Add(20 * time.Hour), duration: 14400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 100*day+20*time.Hour, 120*day+20*time.Hour)},
		},
		{
			name: "timestamps normalized to UTC", start: start.In(time.FixedZone("offset", 5*60*60+30*60)), duration: 14400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("freeze", 100*day, 120*day)},
		},
		{
			name: "leap day and year boundaries", duration: 86400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{
				maintenanceTestExclusion("leap", time.Date(2028, time.February, 20, 2, 0, 0, 0, time.UTC), time.Date(2028, time.March, 21, 2, 0, 0, 0, time.UTC)),
				maintenanceTestExclusion("year", time.Date(2028, time.December, 20, 2, 0, 0, 0, time.UTC), time.Date(2029, time.January, 19, 2, 0, 0, 0, time.UTC)),
			},
		},
		{
			name: "distant exclusions do not require scanning intervening years", duration: 14400, days: allDays,
			exclusions: []ControlPlaneMaintenanceExclusion{
				maintenanceTestExclusion("distant", time.Date(4000, time.January, 1, 2, 0, 0, 0, time.UTC), time.Date(4000, time.January, 21, 2, 0, 0, 0, time.UTC)),
				maintenanceTestExclusion("farther", time.Date(9000, time.January, 1, 2, 0, 0, 0, time.UTC), time.Date(9000, time.January, 21, 2, 0, 0, 0, time.UTC)),
			},
		},
		{
			name: "exclusions before activation do not reduce availability", duration: 43200, days: []string{"monday"},
			exclusions: []ControlPlaneMaintenanceExclusion{exclusion("past", -30*day, 0)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := validClusterWithWindow()
			cluster.Spec.Maintenance.Exclusions = &tc.exclusions
			cluster.Spec.Maintenance.Window.StartTime = metav1.NewTime(start)
			if !tc.start.IsZero() {
				cluster.Spec.Maintenance.Window.StartTime = metav1.NewTime(tc.start)
			}
			cluster.Spec.Maintenance.Window.DurationSeconds = tc.duration
			cluster.Spec.Maintenance.Window.Recurrence.DaysOfWeek = tc.days
			wantError := ""
			if tc.invalid {
				wantError = "at least 48 hours"
			}
			assertMaintenanceClusterValidation(t, cluster, wantError)
		})
	}
}

func TestClusterMaintenanceAvailabilityUpdates(t *testing.T) {
	const day = 24 * time.Hour
	for _, tc := range []struct {
		name      string
		mutate    func(*Cluster)
		wantError string
	}{
		{name: "remove exclusions", mutate: func(p *Cluster) { p.Spec.Maintenance.Exclusions = nil }},
		{name: "remove window", mutate: func(p *Cluster) { p.Spec.Maintenance.Window = nil }},
		{
			name: "shorten exclusion",
			mutate: func(p *Cluster) {
				(*p.Spec.Maintenance.Exclusions)[0].EndTime.Time = (*p.Spec.Maintenance.Exclusions)[0].EndTime.Add(-day)
			},
		},
		{
			name: "add exclusion outside maintenance hours",
			mutate: func(p *Cluster) {
				start := (*p.Spec.Maintenance.Exclusions)[0].EndTime.Add(6 * time.Hour)
				*p.Spec.Maintenance.Exclusions = append(*p.Spec.Maintenance.Exclusions, maintenanceTestExclusion("extra", start, start.Add(time.Hour)))
			},
		},
		{
			name: "extend exclusion across availability boundary",
			mutate: func(p *Cluster) {
				(*p.Spec.Maintenance.Exclusions)[0].EndTime.Time = (*p.Spec.Maintenance.Exclusions)[0].EndTime.Add(time.Second)
			},
			wantError: "at least 48 hours",
		},
		{
			name: "add exclusion across availability boundary",
			mutate: func(p *Cluster) {
				start := (*p.Spec.Maintenance.Exclusions)[0].EndTime.Time
				*p.Spec.Maintenance.Exclusions = append(*p.Spec.Maintenance.Exclusions, maintenanceTestExclusion("extra", start, start.Add(time.Second)))
			},
			wantError: "at least 48 hours",
		},
		{
			name:      "shorten maintenance window",
			mutate:    func(p *Cluster) { p.Spec.Maintenance.Window.DurationSeconds-- },
			wantError: "at least 48 hours",
		},
		{
			name: "remove a maintenance weekday",
			mutate: func(p *Cluster) {
				p.Spec.Maintenance.Window.Recurrence.DaysOfWeek = p.Spec.Maintenance.Window.Recurrence.DaysOfWeek[:6]
			},
			wantError: "at least 48 hours",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := validClusterWithWindow()
			start := old.Spec.Maintenance.Window.StartTime.Add(100 * day)
			old.Spec.Maintenance.Exclusions = &[]ControlPlaneMaintenanceExclusion{maintenanceTestExclusion("freeze", start, start.Add(20*day))}
			updated := old.DeepCopy()
			tc.mutate(updated)
			err := updated.ValidateUpdate(t.Context(), old)
			assertMaintenanceValidationError(t, err, tc.wantError)
		})
	}
	t.Run("adding window revalidates existing exclusions", func(t *testing.T) {
		old := validClusterWithWindow()
		start := old.Spec.Maintenance.Window.StartTime.Time
		old.Spec.Maintenance.Window = nil
		old.Spec.Maintenance.Exclusions = &[]ControlPlaneMaintenanceExclusion{maintenanceTestExclusion("freeze", start, start.Add(30*day))}
		if err := old.ValidateCreate(t.Context()); err != nil {
			t.Fatal(err)
		}
		updated := old.DeepCopy()
		updated.Spec.Maintenance.Window = validClusterWithWindow().Spec.Maintenance.Window
		assertMaintenanceValidationError(t, updated.ValidateUpdate(t.Context(), old), "at least 48 hours")
	})
}

func maintenanceTestExclusion(name string, start, end time.Time) ControlPlaneMaintenanceExclusion {
	return ControlPlaneMaintenanceExclusion{Name: name, StartTime: metav1.NewTime(start), EndTime: metav1.NewTime(end)}
}

func assertMaintenanceClusterValidation(t *testing.T, cluster *Cluster, wantError string) {
	t.Helper()
	assertMaintenanceValidationError(t, cluster.ValidateCreate(t.Context()), wantError)
	old := validClusterWithWindow()
	assertMaintenanceValidationError(t, cluster.ValidateUpdate(t.Context(), old), wantError)
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
