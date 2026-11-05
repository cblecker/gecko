package v1

import (
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	maintenanceAvailabilityPeriod  = 32 * 24 * time.Hour
	minimumMaintenanceAvailability = 48 * time.Hour
)

type maintenanceInterval struct {
	start time.Time `json:"-"`
	end   time.Time `json:"-"`
}

// validateMaintenanceAvailability runs after structural validation and includes
// any initial wait from now until the configured window's activation.
// Without a window, all time outside exclusions is available for maintenance.
func validateMaintenanceAvailability(window *ControlPlaneMaintenanceWindow, exclusions []ControlPlaneMaintenanceExclusion, now time.Time) error {
	if window == nil && len(exclusions) == 0 {
		return nil
	}
	now = now.UTC()
	startsInFuture := window != nil && window.StartTime.After(now)
	blackouts := make([]maintenanceInterval, 0, len(exclusions))
	for _, exclusion := range exclusions {
		blackouts = append(blackouts, maintenanceInterval{exclusion.StartTime.UTC(), exclusion.EndTime.UTC()})
	}
	slices.SortFunc(blackouts, func(a, b maintenanceInterval) int { return a.start.Compare(b.start) })
	if window == nil {
		// Model unrestricted timing as consecutive 24-hour occurrences. Periods
		// ending before the first blackout cannot lose any availability.
		window = &ControlPlaneMaintenanceWindow{
			StartTime:       metav1.NewTime(blackouts[0].start.Add(-maintenanceAvailabilityPeriod)),
			DurationSeconds: 86400,
			Recurrence: ControlPlaneMaintenanceRecurrence{
				Frequency:  "weekly",
				DaysOfWeek: []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"},
			},
		}
	}
	activation := window.StartTime.UTC()

	// Without exclusions the schedule repeats every week, apart from its initial
	// activation. One week of rolling-period starts covers that baseline. An
	// exclusion can only affect periods starting between its start minus 32 days
	// and its end. Checking those bounded regions avoids scanning gaps of years
	// between exclusions; the API limits each exclusion to 30 days.
	var regions []maintenanceInterval
	if startsInFuture {
		// No maintenance is available before activation. For rolling starts
		// between now and activation, availability can only increase as the
		// right edge advances, so the period starting now is the minimum.
		regions = append(regions, maintenanceInterval{now, now})
	}
	regions = append(regions, maintenanceInterval{activation, activation.Add(7 * 24 * time.Hour)})
	for _, blackout := range blackouts {
		if !blackout.end.After(activation) {
			continue
		}
		regions = append(regions, maintenanceInterval{
			start: laterMaintenanceTime(activation, blackout.start.Add(-maintenanceAvailabilityPeriod)),
			end:   blackout.end,
		})
	}

	for _, region := range regions {
		available := maintenanceAvailableIntervals(window, blackouts, region.start, region.end.Add(maintenanceAvailabilityPeriod))
		candidates := []time.Time{region.start, region.end}
		// Availability in [t, t+32d) is continuous and piecewise linear. Its slope
		// changes only when either edge crosses an available interval boundary,
		// so these points include every possible minimum, without time sampling.
		for _, interval := range available {
			for _, boundary := range []time.Time{interval.start, interval.end} {
				for _, candidate := range []time.Time{boundary, boundary.Add(-maintenanceAvailabilityPeriod)} {
					if !candidate.Before(region.start) && !candidate.After(region.end) {
						candidates = append(candidates, candidate)
					}
				}
			}
		}
		slices.SortFunc(candidates, time.Time.Compare)
		candidates = slices.Compact(candidates)
		for _, start := range candidates {
			end := start.Add(maintenanceAvailabilityPeriod)
			var duration time.Duration
			for _, interval := range available {
				from := laterMaintenanceTime(start, interval.start)
				to := earlierMaintenanceTime(end, interval.end)
				if from.Before(to) {
					duration += to.Sub(from)
				}
			}
			if duration < minimumMaintenanceAvailability {
				return fmt.Errorf("spec.maintenance.window and spec.maintenance.exclusions must leave at least 48 hours of maintenance availability in every rolling 32-day period; only %s remains between %s and %s",
					duration, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
			}
		}
	}
	return nil
}

// maintenanceAvailableIntervals returns disjoint, half-open UTC intervals after
// subtracting blackouts. Advancing the cursor through sorted blackouts counts
// overlapping or adjacent exclusions only once.
func maintenanceAvailableIntervals(window *ControlPlaneMaintenanceWindow, blackouts []maintenanceInterval, from, to time.Time) []maintenanceInterval {
	activation := window.StartTime.UTC()
	duration := time.Duration(window.DurationSeconds) * time.Second
	days := make(map[string]bool, len(window.Recurrence.DaysOfWeek))
	for _, day := range window.Recurrence.DaysOfWeek {
		days[day] = true
	}
	// A window lasts at most 24 hours, so the previous day's occurrence is the
	// only one that could have started before this region and still overlap it.
	day := time.Date(from.Year(), from.Month(), from.Day(), activation.Hour(), activation.Minute(), activation.Second(), activation.Nanosecond(), time.UTC).AddDate(0, 0, -1)
	var intervals []maintenanceInterval
	for ; day.Before(to); day = day.AddDate(0, 0, 1) {
		if day.Before(activation) || !days[strings.ToLower(day.Weekday().String())] {
			continue
		}
		cursor := laterMaintenanceTime(day, from)
		end := earlierMaintenanceTime(day.Add(duration), to)
		for _, blackout := range blackouts {
			if !blackout.end.After(cursor) {
				continue
			}
			if !blackout.start.Before(end) {
				break
			}
			if cursor.Before(blackout.start) {
				intervals = append(intervals, maintenanceInterval{cursor, blackout.start})
			}
			cursor = laterMaintenanceTime(cursor, blackout.end)
		}
		if cursor.Before(end) {
			intervals = append(intervals, maintenanceInterval{cursor, end})
		}
	}
	return intervals
}

func laterMaintenanceTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlierMaintenanceTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
