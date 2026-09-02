package optimize

import (
	"fmt"
	"sort"
	"strings"
)

// This file declares, per action, what an executor for that action reads out
// of Recommendation.Parameters.
//
// It exists because the contract used to live nowhere. Recommendation's own
// doc comment states the direction it runs in — parameter keys are the
// executor's vocabulary, not the rule's — but a doc comment cannot be
// asserted, and five rule/executor pairs had quietly drifted apart:
// target_instance_type against an action with no instance-type field at all,
// services []string against service_name string, active_hours_utc against
// schedule, target_class against transition_storage_class,
// target_storage_type against storage_type. Every one of those produced a
// recommendation that passed policy, passed approval, passed preflight, and
// failed at the mutate step — or, worse, succeeded while doing nothing.
//
// Declaring the contract here rather than in either adapter is deliberate.
// Two adapters implement each action (the live AWS executor and the
// simulator) and both must read the same keys, so the contract cannot live
// in one of them without the other being free to drift. It sits beside
// ActionType, which is the closed vocabulary this same package already owns.
//
// Traceability: REQ-OPT-016, SPEC-OPT-010.

// ParameterContract states what a recommendation must carry for an action's
// executor to be able to act on it.
type ParameterContract struct {
	// Required keys must all be present.
	Required []string
	// AnyOf, when non-empty, requires at least one of its keys. It expresses
	// actions that take a menu of independent changes — modify_rds_storage
	// can change size, type, IOPS or any combination — where naming none of
	// them is the only invalid request.
	AnyOf []string
	// Together lists key groups that are all-or-nothing: present any member
	// and every member must be present. A lifecycle transition needs both a
	// day count and a destination class; either one alone builds a rule S3
	// rejects or silently ignores.
	Together [][]string
	// Note explains anything a reader of a failing assertion would otherwise
	// have to go read an adapter to understand.
	Note string
}

// Satisfied reports whether a parameter map meets the contract, returning a
// human-readable reason when it does not. The reason is written for whoever
// is looking at a failing contract test, so it names the action, the key and
// what the key is for.
func (c ParameterContract) Satisfied(action ActionType, params map[string]any) (bool, string) {
	present := func(k string) bool {
		v, ok := params[k]
		if !ok {
			return false
		}
		// An empty string is not a value. A rule that computes a target and
		// gets "" has failed to compute it, and passing that on lets the
		// executor fall back to a default that is not what the recommendation
		// promised.
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			return false
		}
		return true
	}

	var missing []string
	for _, k := range c.Required {
		if !present(k) {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return false, fmt.Sprintf("%s requires parameter(s) %s; got keys %s",
			action, strings.Join(missing, ", "), sortedKeys(params))
	}
	if len(c.AnyOf) > 0 {
		found := false
		for _, k := range c.AnyOf {
			if present(k) {
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Sprintf("%s requires at least one of %s; got keys %s",
				action, strings.Join(c.AnyOf, ", "), sortedKeys(params))
		}
	}
	for _, group := range c.Together {
		any, all := false, true
		for _, k := range group {
			if present(k) {
				any = true
			} else {
				all = false
			}
		}
		if any && !all {
			return false, fmt.Sprintf("%s takes %s together or not at all; got keys %s",
				action, strings.Join(group, " + "), sortedKeys(params))
		}
	}
	return true, ""
}

func sortedKeys(params map[string]any) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "(none)"
	}
	return strings.Join(keys, ", ")
}

// actionParameterContracts covers exactly the actions an executor in this
// codebase implements. An action absent from this map is one no executor can
// perform yet: ParameterContractFor reports that plainly, and the contract
// test asserts the map and the executor registries agree in both directions,
// so building an executor without declaring its parameters — or declaring
// parameters for an action nothing implements — fails rather than drifting.
//
// Actions whose contract is an empty ParameterContract are not oversights:
// stopping an instance, deleting an unattached volume or releasing an idle
// address are fully specified by the target the plan already carries, and
// they take no further input.
var actionParameterContracts = map[ActionType]ParameterContract{
	ActionResizeInstance:   {Required: []string{"instance_type"}},
	ActionStopInstance:     {Note: "fully specified by the plan's target instance"},
	ActionScheduleShutdown: {Required: []string{"schedule"}, Note: "stored verbatim as the cloudoptix:schedule tag and compared against on the idempotency check"},

	ActionDeleteVolume:     {Note: "fully specified by the plan's target volume"},
	ActionResizeVolume:     {Required: []string{"size_gib"}},
	ActionModifyVolumeType: {Required: []string{"volume_type"}},
	ActionDeleteSnapshot:   {Note: "fully specified by the plan's target snapshot"},
	ActionReleaseElasticIP: {Note: "fully specified by the plan's target allocation"},

	ActionResizeRDS: {Required: []string{"instance_class"}},
	ActionModifyRDSStorage: {
		AnyOf: []string{"allocated_storage_gb", "storage_type", "iops"},
		Note:  "the executor changes whichever of these it is given and refuses a request that names none",
	},

	ActionResizeNodeGroup: {
		Required: []string{"desired_size"},
		Note:     "eks:UpdateNodegroupConfig changes the scaling configuration only; a node group's instance type is fixed at creation",
	},

	ActionApplyS3Lifecycle: {
		AnyOf:    []string{"transition_days", "expiration_days", "noncurrent_expiration_days", "abort_incomplete_multipart_days"},
		Together: [][]string{{"transition_days", "transition_storage_class"}},
		Note:     "manages one rule inside the bucket's lifecycle configuration, keyed by rule_id; a rule with no clause is an enabled no-op",
	},
	ActionAbortMultipartUploads: {
		Required: []string{"older_than_days"},
		Note:     "the age filter is what keeps this from aborting a customer's in-flight upload",
	},
	ActionSetLogRetention: {Required: []string{"retention_days"}},

	ActionResizeLambdaMemory:           {Required: []string{"memory_mb"}},
	ActionRemoveProvisionedConcurrency: {Note: "fully specified by the plan's target function"},

	ActionCreateVPCEndpoint: {
		Required: []string{"service_name"},
		Note:     "one gateway endpoint per plan, named the way ec2:CreateVpcEndpoint names it (com.amazonaws.<region>.<service>)",
	},
}

// ParameterContractFor returns the parameter contract for an action, and
// whether one is declared. ok=false means no executor in this codebase can
// perform the action — the recommendation is presented to a human as
// non-executable, which is what Recommendation's own doc comment describes
// and what the UI is expected to say plainly.
func ParameterContractFor(a ActionType) (ParameterContract, bool) {
	c, ok := actionParameterContracts[a]
	return c, ok
}

// ExecutableActions lists every action a parameter contract is declared for,
// sorted, so a test can compare it against the executor registries without
// depending on map order.
func ExecutableActions() []ActionType {
	out := make([]ActionType, 0, len(actionParameterContracts))
	for a := range actionParameterContracts {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
