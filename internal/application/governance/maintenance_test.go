package governance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

func TestInMaintenanceWindow(t *testing.T) {
	sp := spec.Spec{}
	sp.Automation.MaintenanceWindows = []spec.MaintenanceWindow{
		{Name: "friday-night", Days: []string{"friday"}, StartUTC: "23:00", DurationMinutes: 120,
			Environments: []string{"production"}},
		{Name: "weeknight-any-env", Days: []string{"mon", "tue", "wed", "thu"}, StartUTC: "02:00", DurationMinutes: 60},
	}

	cases := []struct {
		name string
		now  time.Time
		env  core.Environment
		want bool
	}{
		{"before window start", time.Date(2026, 9, 4, 22, 59, 0, 0, time.UTC), core.EnvProduction, false}, // Friday
		{"exactly at window start (inclusive)", time.Date(2026, 9, 4, 23, 0, 0, 0, time.UTC), core.EnvProduction, true},
		{"mid window", time.Date(2026, 9, 4, 23, 30, 0, 0, time.UTC), core.EnvProduction, true},
		{"wraps past midnight into Saturday", time.Date(2026, 9, 5, 0, 30, 0, 0, time.UTC), core.EnvProduction, true},
		{"exactly at window end (exclusive)", time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC), core.EnvProduction, false},
		{"one minute past window end", time.Date(2026, 9, 5, 1, 1, 0, 0, time.UTC), core.EnvProduction, false},
		{"wrong environment", time.Date(2026, 9, 4, 23, 30, 0, 0, time.UTC), core.EnvDevelopment, false},
		{"different window, mid-week, any env", time.Date(2026, 9, 1, 2, 15, 0, 0, time.UTC), core.EnvStaging, true}, // Tuesday
		{"different window, wrong day", time.Date(2026, 9, 4, 2, 15, 0, 0, time.UTC), core.EnvStaging, false},        // Friday, only mon-thu declared
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := InMaintenanceWindow(sp, tc.env, tc.now)
			assert.Equal(t, tc.want, ok)
		})
	}
}

func TestInMaintenanceWindow_NoDeclaredWindowsNeverMatches(t *testing.T) {
	// A tenant with no declared windows has none — this must never fall back
	// to "always allowed".
	sp := spec.Spec{}
	_, ok := InMaintenanceWindow(sp, core.EnvProduction, testNow)
	assert.False(t, ok)
}

func TestInMaintenanceWindow_UnparsableStartTimeIsSkippedNotFatal(t *testing.T) {
	sp := spec.Spec{}
	sp.Automation.MaintenanceWindows = []spec.MaintenanceWindow{
		{Name: "broken", StartUTC: "not-a-time", DurationMinutes: 60},
	}
	_, ok := InMaintenanceWindow(sp, core.EnvProduction, testNow)
	assert.False(t, ok)
}

func TestInChangeFreeze(t *testing.T) {
	sp := spec.Spec{}
	sp.Governance.ChangeFreezeWindows = []string{"2026-12-20..2026-12-31", "not-a-valid-window"}

	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"before freeze", time.Date(2026, 12, 19, 23, 59, 59, 0, time.UTC), false},
		{"start of freeze", time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC), true},
		{"mid freeze", time.Date(2026, 12, 25, 12, 0, 0, 0, time.UTC), true},
		{"last instant of freeze (inclusive end date)", time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), true},
		{"just after freeze ends", time.Date(2027, 1, 1, 0, 0, 1, 0, time.UTC), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := InChangeFreeze(sp, tc.now)
			assert.Equal(t, tc.want, ok)
		})
	}
}

func TestParseFreezeWindow_RejectsMalformedInput(t *testing.T) {
	_, _, ok := ParseFreezeWindow("garbage")
	assert.False(t, ok)

	_, _, ok = ParseFreezeWindow("2026-12-31..2026-12-01") // end before start
	assert.False(t, ok)

	start, end, ok := ParseFreezeWindow("2026-01-01..2026-01-03")
	assert.True(t, ok)
	assert.True(t, start.Before(end))
}
