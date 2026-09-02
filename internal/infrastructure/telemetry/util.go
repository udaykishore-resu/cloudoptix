package telemetry

import "fmt"

// panicToError converts a recovered panic value into an error, used by
// InstrumentJob's panic boundary so a job's panic becomes an observable
// error return rather than taking down the worker goroutine.
func panicToError(r any) error {
	if err, ok := r.(error); ok {
		return fmt.Errorf("telemetry: job panicked: %w", err)
	}
	return fmt.Errorf("telemetry: job panicked: %v", r)
}
