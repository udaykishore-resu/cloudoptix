package governance

import (
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// InMaintenanceWindow reports whether now falls inside one of the tenant's
// declared maintenance windows for the given environment, and which one.
//
// A tenant with no declared windows has none — this never falls back to
// "always" or "business hours", because silently assuming permission is the
// wrong failure mode for a change-timing guard: every window a resource
// change can land in must be one a human explicitly wrote into the approved
// specification.
//
// Every window is evaluated against two candidate start days — the
// instant's own UTC calendar day and the day before it — rather than one.
// A naive single-day check (does today's weekday match, does the
// minute-of-day fall in [start, start+duration)) silently drops the tail of
// any window whose start-plus-duration crosses midnight: a Friday 23:00
// window lasting 120 minutes is a real, common maintenance window (start
// late Friday, finish just after midnight Saturday), and a tenant who
// declared "fri 23:00 for 2h" has every right to expect 00:30 UTC Saturday
// to still be inside it. Checking "did a window that started yesterday
// still cover this instant" alongside "did a window that started today"
// handles that wraparound correctly without needing the window's Days field
// to enumerate both days itself.
func InMaintenanceWindow(sp spec.Spec, env core.Environment, now time.Time) (spec.MaintenanceWindow, bool) {
	u := now.UTC()
	for _, w := range sp.Automation.MaintenanceWindows {
		if !windowAppliesToEnvironment(w, env) {
			continue
		}
		start, err := parseHHMM(w.StartUTC)
		if err != nil {
			continue // an unparsable window can never be entered; validation catches this at save time
		}
		for _, dayOffset := range []int{0, -1} {
			candidateDay := u.AddDate(0, 0, dayOffset)
			if !windowAppliesToDay(w, candidateDay) {
				continue
			}
			windowStart := time.Date(candidateDay.Year(), candidateDay.Month(), candidateDay.Day(),
				start.hour, start.minute, 0, 0, time.UTC)
			windowEnd := windowStart.Add(time.Duration(w.DurationMinutes) * time.Minute)
			if !u.Before(windowStart) && u.Before(windowEnd) {
				return w, true
			}
		}
	}
	return spec.MaintenanceWindow{}, false
}

func windowAppliesToEnvironment(w spec.MaintenanceWindow, env core.Environment) bool {
	if len(w.Environments) == 0 {
		return true
	}
	for _, e := range w.Environments {
		if strings.EqualFold(e, string(env)) {
			return true
		}
	}
	return false
}

func windowAppliesToDay(w spec.MaintenanceWindow, day time.Time) bool {
	if len(w.Days) == 0 {
		return true
	}
	weekday := strings.ToLower(day.Weekday().String())
	for _, d := range w.Days {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == weekday || (len(d) == 3 && d == weekday[:3]) {
			return true
		}
	}
	return false
}

type hhmm struct{ hour, minute int }

func parseHHMM(s string) (hhmm, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return hhmm{}, err
	}
	return hhmm{hour: t.Hour(), minute: t.Minute()}, nil
}

// InChangeFreeze reports whether now falls inside one of the tenant's
// declared change-freeze windows, and which one, as the raw string from the
// specification (for the decision's explanation text).
//
// Change-freeze windows are a distinct concept from maintenance windows —
// a holiday code freeze or a quarter-close blackout is a calendar-date range
// spanning days or weeks, not a recurring weekly slot — so
// Governance.ChangeFreezeWindows entries use their own format:
// "YYYY-MM-DD..YYYY-MM-DD", an inclusive UTC date range (the end date runs
// through 23:59:59 UTC). An entry that does not parse in this shape is
// skipped rather than treated as an error here: spec.Validate is where a
// malformed freeze window is caught and reported to the tenant, and a
// governance evaluation must never fail merely because one unrelated
// specification field was mistyped — see spec.Validate's own freeze-window
// checks for the authoritative format documentation surfaced to the user.
func InChangeFreeze(sp spec.Spec, now time.Time) (string, bool) {
	u := now.UTC()
	for _, raw := range sp.Governance.ChangeFreezeWindows {
		start, end, ok := parseFreezeWindow(raw)
		if !ok {
			continue
		}
		if !u.Before(start) && u.Before(end) {
			return raw, true
		}
	}
	return "", false
}

// ParseFreezeWindow exposes the change-freeze date-range parser so
// spec.Validate-style callers and tests outside this package can check a
// window string is well-formed without duplicating the format.
func ParseFreezeWindow(raw string) (start, end time.Time, ok bool) { return parseFreezeWindow(raw) }

func parseFreezeWindow(raw string) (time.Time, time.Time, bool) {
	parts := strings.SplitN(raw, "..", 2)
	if len(parts) != 2 {
		return time.Time{}, time.Time{}, false
	}
	start, err1 := time.Parse("2006-01-02", strings.TrimSpace(parts[0]))
	endDay, err2 := time.Parse("2006-01-02", strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return time.Time{}, time.Time{}, false
	}
	// The end date is inclusive through its final second, not its midnight
	// boundary — "2026-12-24..2026-12-26" must still be in effect at 23:00 on
	// the 26th, which a half-open [start, endDay-midnight) interval would
	// wrongly exclude.
	end := endDay.Add(24*time.Hour - time.Nanosecond)
	if end.Before(start) {
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

// resourceExcluded reports whether the specification's exclusion lists match
// this recommendation's resource, by CloudOptix ID, native provider ID, or
// any declared excluded tag. Matching by both id forms is deliberate: a
// tenant writing a specification by hand knows native ids (i-0abc123) far
// more often than CloudOptix's internal ones.
func resourceExcluded(excludedResources []string, resourceID core.ID, nativeID string) bool {
	for _, x := range excludedResources {
		if x == string(resourceID) || x == nativeID {
			return true
		}
	}
	return false
}

func actionExcluded(excludedActions []string, action string) bool {
	for _, x := range excludedActions {
		if strings.EqualFold(strings.TrimSpace(x), action) {
			return true
		}
	}
	return false
}

func tagsExcluded(excludedTags map[string]string, tags core.Tags) (string, bool) {
	for k, want := range excludedTags {
		got, ok := tags.Get(k)
		if !ok {
			continue
		}
		if want == "" || want == "*" || strings.EqualFold(got, want) {
			return k + "=" + got, true
		}
	}
	return "", false
}
