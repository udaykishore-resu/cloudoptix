package awssim

import (
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// resizeInstanceSpec changes an EC2 instance's InstanceType in place. A real
// ModifyInstanceAttribute call requires the instance to be stopped first;
// the simulator folds that stop/modify/start dance into a single mutate
// step rather than modeling three separate AWS calls, which keeps the
// story about the rightsizing decision (was m5.4xlarge, is now m5.xlarge)
// rather than about EC2's state machine.
var resizeInstanceSpec = actionSpec{
	action:          optimize.ActionResizeInstance,
	requiredActions: []string{"ec2:DescribeInstances", "ec2:ModifyInstanceAttribute", "ec2:StopInstances", "ec2:StartInstances"},
	kind:            cloud.KindEC2Instance,
	awsAction:       "ec2:ModifyInstanceAttribute",
	titleFmt:        "Resize EC2 instance %s to a smaller type",

	rollbackFeasible: true,
	dataLossRisk:     core.RiskLow,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		inst, ok := e.EC2Instances[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"instance_type": inst.InstanceType}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		inst, ok := e.EC2Instances[id]
		target, pok := paramStr(params, "instance_type")
		return ok && pok && inst.InstanceType == target
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		inst := e.EC2Instances[id]
		target, ok := paramStr(params, "instance_type")
		if !ok || target == "" {
			return nil, core.Invalid("resize_instance requires a non-empty instance_type parameter")
		}
		if _, known := e.Catalog.InstanceSpec(target); !known {
			return nil, core.Invalid("unknown instance type %q", target)
		}
		prev := inst.InstanceType
		inst.InstanceType = target
		return map[string]any{
			"instance_type": target, "previous_instance_type": prev,
			"new_monthly_cost_micros": e.InstanceMonthlyCost(inst).Micros(),
		}, nil
	},
	restore: func(e *Estate, id string, before map[string]any) error {
		inst, ok := e.EC2Instances[id]
		if !ok {
			return core.NotFound("ec2_instance", id)
		}
		t, ok := paramStr(before, "instance_type")
		if !ok || t == "" {
			return core.Invalid("rollback snapshot for %s is missing instance_type", id)
		}
		inst.InstanceType = t
		return nil
	},
}

// stopInstanceSpec stops a running EC2 instance, dropping its compute
// charge to zero (InstanceMonthlyCost prices only running/pending
// instances; its volumes keep billing separately, matching AWS).
var stopInstanceSpec = actionSpec{
	action:          optimize.ActionStopInstance,
	requiredActions: []string{"ec2:DescribeInstances", "ec2:StopInstances"},
	kind:            cloud.KindEC2Instance,
	awsAction:       "ec2:StopInstances",
	titleFmt:        "Stop idle EC2 instance %s",

	rollbackFeasible: true,
	dataLossRisk:     core.RiskNone,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		inst, ok := e.EC2Instances[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"state": string(inst.State)}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		inst, ok := e.EC2Instances[id]
		return ok && inst.State == cloud.StateStopped
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		inst := e.EC2Instances[id]
		inst.State = cloud.StateStopped
		now := time.Now().UTC()
		inst.StoppedAt = &now
		return map[string]any{"state": string(cloud.StateStopped), "new_monthly_cost_micros": e.InstanceMonthlyCost(inst).Micros()}, nil
	},
	restore: func(e *Estate, id string, before map[string]any) error {
		inst, ok := e.EC2Instances[id]
		if !ok {
			return core.NotFound("ec2_instance", id)
		}
		s, _ := paramStr(before, "state")
		if s == "" {
			s = string(cloud.StateRunning)
		}
		inst.State = cloud.State(s)
		if inst.State == cloud.StateRunning {
			inst.StoppedAt = nil
		}
		return nil
	},
}

// scheduleShutdownSpec tags an instance with an off-hours shutdown window
// and, to make the saving immediately visible in a demo rather than only
// at the next scheduled boundary, stops it now. Rollback removes the tag
// and restores whatever run state preceded scheduling.
var scheduleShutdownSpec = actionSpec{
	action:          optimize.ActionScheduleShutdown,
	requiredActions: []string{"ec2:DescribeInstances", "ec2:CreateTags", "ec2:StopInstances"},
	kind:            cloud.KindEC2Instance,
	awsAction:       "ec2:CreateTags",
	titleFmt:        "Schedule off-hours shutdown for %s",

	rollbackFeasible: true,
	dataLossRisk:     core.RiskLow,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		inst, ok := e.EC2Instances[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"state": string(inst.State), "schedule_tag": inst.Tags["cloudoptix:schedule"]}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		inst, ok := e.EC2Instances[id]
		if !ok {
			return false
		}
		schedule, _ := paramStr(params, "schedule")
		if schedule == "" {
			schedule = defaultShutdownSchedule
		}
		return inst.State == cloud.StateStopped && inst.Tags["cloudoptix:schedule"] == schedule
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		inst := e.EC2Instances[id]
		schedule, _ := paramStr(params, "schedule")
		if schedule == "" {
			schedule = defaultShutdownSchedule
		}
		if inst.Tags == nil {
			inst.Tags = core.Tags{}
		}
		inst.Tags["cloudoptix:schedule"] = schedule
		inst.State = cloud.StateStopped
		now := time.Now().UTC()
		inst.StoppedAt = &now
		return map[string]any{
			"schedule": schedule, "state": string(cloud.StateStopped),
			"new_monthly_cost_micros": e.InstanceMonthlyCost(inst).Micros(),
		}, nil
	},
	restore: func(e *Estate, id string, before map[string]any) error {
		inst, ok := e.EC2Instances[id]
		if !ok {
			return core.NotFound("ec2_instance", id)
		}
		prevSchedule, _ := paramStr(before, "schedule_tag")
		if prevSchedule == "" {
			delete(inst.Tags, "cloudoptix:schedule")
		} else {
			if inst.Tags == nil {
				inst.Tags = core.Tags{}
			}
			inst.Tags["cloudoptix:schedule"] = prevSchedule
		}
		prevState, _ := paramStr(before, "state")
		if prevState == "" {
			prevState = string(cloud.StateRunning)
		}
		inst.State = cloud.State(prevState)
		if inst.State == cloud.StateRunning {
			inst.StoppedAt = nil
		}
		return nil
	},
}

// defaultShutdownSchedule is used when a recommendation does not specify
// its own off-hours window.
const defaultShutdownSchedule = "stop 19:00-07:00 Mon-Fri, all day Sat-Sun"

// resizeRDSSpec changes an RDS/Aurora instance's InstanceClass in place.
var resizeRDSSpec = actionSpec{
	action:          optimize.ActionResizeRDS,
	requiredActions: []string{"rds:DescribeDBInstances", "rds:ModifyDBInstance"},
	kind:            cloud.KindRDSInstance,
	awsAction:       "rds:ModifyDBInstance",
	titleFmt:        "Resize RDS instance %s to a smaller class",

	rollbackFeasible: true,
	dataLossRisk:     core.RiskMedium, // a class change can trigger a brief failover

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		r, ok := e.RDSInstances[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"instance_class": r.InstanceClass}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		r, ok := e.RDSInstances[id]
		target, pok := paramStr(params, "instance_class")
		return ok && pok && r.InstanceClass == target
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		r := e.RDSInstances[id]
		target, ok := paramStr(params, "instance_class")
		if !ok || target == "" {
			return nil, core.Invalid("resize_rds_instance requires a non-empty instance_class parameter")
		}
		prev := r.InstanceClass
		r.InstanceClass = target
		return map[string]any{
			"instance_class": target, "previous_instance_class": prev,
			"new_monthly_cost_micros": e.RDSInstanceMonthlyCost(r).Micros(),
		}, nil
	},
	restore: func(e *Estate, id string, before map[string]any) error {
		r, ok := e.RDSInstances[id]
		if !ok {
			return core.NotFound("rds_instance", id)
		}
		t, ok := paramStr(before, "instance_class")
		if !ok || t == "" {
			return core.Invalid("rollback snapshot for %s is missing instance_class", id)
		}
		r.InstanceClass = t
		return nil
	},
}

// resizeNodeGroupSpec changes an EKS managed node group's desired size.
// Shrinking a node group tightens its bin-packing (the same pods now sit
// on fewer nodes), which this models by scaling PackedFraction up by the
// same ratio the node count went down, capped at 1.0 (fully packed).
var resizeNodeGroupSpec = actionSpec{
	action:          optimize.ActionResizeNodeGroup,
	requiredActions: []string{"eks:DescribeNodegroup", "eks:UpdateNodegroupConfig"},
	kind:            cloud.KindEKSNodeGroup,
	awsAction:       "eks:UpdateNodegroupConfig",
	titleFmt:        "Resize EKS node group %s to match actual pod demand",

	rollbackFeasible: true,
	dataLossRisk:     core.RiskMedium, // shrinking evicts and reschedules pods

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		ng, ok := e.EKSNodeGroups[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"desired_size": ng.DesiredSize, "packed_fraction": ng.PackedFraction}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		ng, ok := e.EKSNodeGroups[id]
		target, pok := paramInt(params, "desired_size")
		return ok && pok && ng.DesiredSize == target
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		ng := e.EKSNodeGroups[id]
		target, ok := paramInt(params, "desired_size")
		if !ok || target <= 0 {
			return nil, core.Invalid("resize_node_group requires a positive desired_size parameter")
		}
		prev := ng.DesiredSize
		ng.DesiredSize = target
		if prev > 0 {
			ng.PackedFraction = ng.PackedFraction * float64(prev) / float64(target)
			if ng.PackedFraction > 1 {
				ng.PackedFraction = 1
			}
		}
		return map[string]any{
			"desired_size": target, "packed_fraction": ng.PackedFraction,
			"new_monthly_cost_micros": e.NodeGroupMonthlyCost(ng).Micros(),
		}, nil
	},
	restore: func(e *Estate, id string, before map[string]any) error {
		ng, ok := e.EKSNodeGroups[id]
		if !ok {
			return core.NotFound("eks_node_group", id)
		}
		ds, ok := paramInt(before, "desired_size")
		if !ok {
			return core.Invalid("rollback snapshot for %s is missing desired_size", id)
		}
		pf, _ := paramFloat(before, "packed_fraction")
		ng.DesiredSize = ds
		ng.PackedFraction = pf
		return nil
	},
}
