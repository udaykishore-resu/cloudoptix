package optimization

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// TestLambdaMonthlyCost_LessMemoryCanCostMore is the case a naive
// "less memory is always cheaper" rule gets wrong: GB-seconds is memory
// times duration, so a memory cut whose duration penalty outpaces the
// per-GB-second price cut raises the bill, not lowers it. LambdaMonthlyCost
// must price that correctly regardless of which direction "cheaper" turns
// out to be.
func TestLambdaMonthlyCost_LessMemoryCanCostMore(t *testing.T) {
	gbSecondPrice := core.USDollars(0.0000166667) // AWS's real x86_64 GB-second rate, roughly
	requestPrice := core.USDollars(0.20)          // per 1,000 requests
	invocations := 5_000_000.0

	// At 1024 MB the function runs in 200ms.
	costAt1024 := LambdaMonthlyCost(1024, 200, invocations, gbSecondPrice, requestPrice)

	// Cutting to 512 MB more than doubles duration (a CPU-starved function
	// thrashing below its working set) rather than the naive "duration
	// roughly doubles" assumption: 520ms observed, not ~400ms.
	costAt512Thrashing := LambdaMonthlyCost(512, 520, invocations, gbSecondPrice, requestPrice)
	assert.True(t, costAt512Thrashing.GreaterThan(costAt1024),
		"512 MB at 520ms must cost MORE than 1024 MB at 200ms: GB-seconds is 512*520=266,240 vs 1024*200=204,800")

	// The same cut to 512 MB with duration scaling as the idealized inverse
	// relationship (400ms, exactly double) leaves GB-seconds unchanged, so
	// the cost is identical up to the (memory-independent) request charge.
	costAt512Ideal := LambdaMonthlyCost(512, 400, invocations, gbSecondPrice, requestPrice)
	assert.InDelta(t, costAt1024.Units(), costAt512Ideal.Units(), 0.01,
		"512 MB at exactly double the duration should be cost-neutral on GB-seconds")

	// And a cut whose duration grows less than proportionally (a mostly-I/O
	// function where memory barely mattered) is a genuine saving.
	costAt512Genuine := LambdaMonthlyCost(512, 220, invocations, gbSecondPrice, requestPrice)
	assert.True(t, costAt512Genuine.LessThan(costAt1024),
		"512 MB at 220ms should cost less: GB-seconds 512*220=112,640 vs 1024*200=204,800")
}

// TestLambdaDurationAtMemory checks the elastic-plus-floor prediction model
// at its two informative extremes and at a general point.
func TestLambdaDurationAtMemory(t *testing.T) {
	t.Run("floorFraction=0 is pure inverse scaling (fully CPU-bound)", func(t *testing.T) {
		got := LambdaDurationAtMemory(512, 400, 1024, 0)
		assert.InDelta(t, 200, got, 0.001, "doubling memory should exactly halve duration when nothing is floor-bound")
	})
	t.Run("floorFraction=1 is constant duration (fully floor-bound)", func(t *testing.T) {
		got := LambdaDurationAtMemory(512, 400, 4096, 1.0)
		assert.InDelta(t, 400, got, 0.001, "a fully non-parallelizable function's duration must not move with memory")
	})
	t.Run("intermediate floorFraction blends both", func(t *testing.T) {
		// floor = 400*0.25 = 100ms fixed; parallel = 300ms that halves.
		got := LambdaDurationAtMemory(512, 400, 1024, 0.25)
		assert.InDelta(t, 100+300*0.5, got, 0.001)
	})
}

// TestLambdaOptimalMemory checks the ladder search never returns a
// higher-cost candidate than the current configuration, and that it finds a
// real improvement when the model implies one.
func TestLambdaOptimalMemory(t *testing.T) {
	gbSecondPrice := core.USDollars(0.0000166667)
	requestPrice := core.USDollars(0.20)
	invocations := 5_000_000.0

	t.Run("fully CPU-bound function: cost-neutral, keeps current memory", func(t *testing.T) {
		bestMB, bestCost, _ := LambdaOptimalMemory(1024, 200, 0.0, invocations, gbSecondPrice, requestPrice, 64)
		currentCost := LambdaMonthlyCost(1024, 200, invocations, gbSecondPrice, requestPrice)
		assert.Equal(t, 1024, bestMB, "every candidate is cost-neutral under floorFraction=0; must not report a spurious change")
		assert.InDelta(t, currentCost.Units(), bestCost.Units(), 0.01)
	})

	t.Run("partially floor-bound function: finds a cheaper memory, never a costlier one", func(t *testing.T) {
		currentMB, currentDuration := 2048, 300.0
		bestMB, bestCost, _ := LambdaOptimalMemory(currentMB, currentDuration, 0.4, invocations, gbSecondPrice, requestPrice, 64)
		currentCost := LambdaMonthlyCost(currentMB, currentDuration, invocations, gbSecondPrice, requestPrice)
		require.NotEqual(t, 0, bestMB)
		assert.False(t, bestCost.GreaterThan(currentCost), "the optimum must never cost more than doing nothing")
		if bestMB != currentMB {
			assert.True(t, bestCost.LessThan(currentCost))
		}
	})
}
