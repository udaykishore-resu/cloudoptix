// set_log_retention is the one action from awssim's action set (and this
// package's own task list) this file deliberately does not implement: it
// targets logs:PutRetentionPolicy on a CloudWatch Logs log group, and no
// CloudWatch Logs SDK client is among this codebase's allowed dependencies
// (see this package's own doc comment in common.go and the discovery
// package's resourcegroupstagging.go, which faces the identical gap for
// discovery and works around it with a generic ARN sweep — an option that
// does not exist here, since resourcegroupstaggingapi has no mutating
// operations at all).
//
// UnsupportedExecutor exists so that code enumerating "does CloudOptix have
// an executor for action X" gets a real answer with a real explanation
// instead of a silent gap: NewSetLogRetentionExecutor returns one, but it is
// deliberately NOT included in NewExecutors' list (see registry.go) because
// an executor whose every method always fails does not belong among the
// ones that actually work.
package executor

import (
	"context"
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// unsupportedReason is why no real implementation exists.
const logRetentionUnsupportedReason = "no CloudWatch Logs SDK client is available in this deployment; " +
	"set_log_retention needs logs:PutRetentionPolicy, which this codebase cannot call"

// UnsupportedExecutor is a ports.Executor stand-in for an action this
// package cannot perform against real AWS, given its allowed dependencies.
// Every method returns the same explanatory error rather than attempting
// anything.
type UnsupportedExecutor struct {
	action          optimize.ActionType
	requiredActions []string
	reason          string
}

var _ ports.Executor = (*UnsupportedExecutor)(nil)

// NewSetLogRetentionExecutor documents, rather than implements,
// set_log_retention. requiredActions still names the IAM actions a future
// real implementation would need, so the onboarding policy generator and
// permission probe this action would drive are not left silently blank.
func NewSetLogRetentionExecutor() *UnsupportedExecutor {
	return &UnsupportedExecutor{
		action:          optimize.ActionSetLogRetention,
		requiredActions: []string{"logs:DescribeLogGroups", "logs:PutRetentionPolicy"},
		reason:          logRetentionUnsupportedReason,
	}
}

func (u *UnsupportedExecutor) Action() optimize.ActionType { return u.action }
func (u *UnsupportedExecutor) RequiredActions() []string   { return u.requiredActions }

func (u *UnsupportedExecutor) unsupported() error {
	return fmt.Errorf("%w: %s executor is unimplemented: %s", core.ErrUnavailable, u.action, u.reason)
}

func (u *UnsupportedExecutor) Plan(context.Context, ports.ExecutionPlanInput) (execute.Plan, error) {
	return execute.Plan{}, u.unsupported()
}
func (u *UnsupportedExecutor) Preflight(context.Context, ports.AWSSession, execute.Plan) error {
	return u.unsupported()
}
func (u *UnsupportedExecutor) Apply(context.Context, ports.AWSSession, execute.Plan, execute.Step) (map[string]any, error) {
	return nil, u.unsupported()
}
func (u *UnsupportedExecutor) Rollback(context.Context, ports.AWSSession, execute.Plan, execute.Step) error {
	return u.unsupported()
}
