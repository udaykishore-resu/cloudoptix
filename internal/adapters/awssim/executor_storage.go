package awssim

import (
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// deleteVolumeSpec deletes an unattached EBS volume outright. Deletion is
// marked irreversible: once the underlying data is gone, the simulator
// recreating an empty record with the same size would misrepresent what
// actually happened on real AWS, so rollback refuses rather than
// pretending.
var deleteVolumeSpec = actionSpec{
	action:          optimize.ActionDeleteVolume,
	requiredActions: []string{"ec2:DescribeVolumes", "ec2:DeleteVolume"},
	kind:            cloud.KindEBSVolume,
	awsAction:       "ec2:DeleteVolume",
	titleFmt:        "Delete unattached EBS volume %s",

	rollbackFeasible: false,
	infeasibleReason: "a deleted volume's data cannot be recovered by the simulator any more than by AWS itself",
	dataLossRisk:     core.RiskHigh,
	deleteAction:     true,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		v, ok := e.EBSVolumes[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"volume_type": v.VolumeType, "size_gib": v.SizeGiB, "attached_to": v.AttachedTo}, true
	},
	extraPrecondition: func(e *Estate, id string) error {
		v, ok := e.EBSVolumes[id]
		if !ok {
			return nil
		}
		if v.AttachedTo != "" {
			return core.Invalid("volume %s is attached to %s; detach it before deleting", id, v.AttachedTo)
		}
		return nil
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool { return false }, // existing == not yet deleted
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		v := e.EBSVolumes[id]
		freed := e.VolumeMonthlyCost(v)
		delete(e.EBSVolumes, id)
		return map[string]any{"deleted": true, "freed_monthly_cost_micros": freed.Micros()}, nil
	},
}

// modifyVolumeTypeSpec changes an EBS volume's type (the gp2-to-gp3
// migration is the common case) and, optionally, its provisioned IOPS and
// throughput. This is a live, online EBS operation in real AWS — no
// detach or stop required — so it needs only one mutate step.
var modifyVolumeTypeSpec = actionSpec{
	action:          optimize.ActionModifyVolumeType,
	requiredActions: []string{"ec2:DescribeVolumes", "ec2:ModifyVolume"},
	kind:            cloud.KindEBSVolume,
	awsAction:       "ec2:ModifyVolume",
	titleFmt:        "Migrate EBS volume %s to gp3",

	rollbackFeasible: true,
	dataLossRisk:     core.RiskNone,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		v, ok := e.EBSVolumes[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"volume_type": v.VolumeType, "iops": v.IOPS, "throughput_mibps": v.ThroughputMiBps}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		v, ok := e.EBSVolumes[id]
		if !ok {
			return false
		}
		target, pok := paramStr(params, "volume_type")
		if !pok || v.VolumeType != target {
			return false
		}
		if iops, ok := paramInt(params, "iops"); ok && int64(iops) != v.IOPS {
			return false
		}
		if tp, ok := paramFloat(params, "throughput_mibps"); ok && tp != v.ThroughputMiBps {
			return false
		}
		return true
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		v := e.EBSVolumes[id]
		target, ok := paramStr(params, "volume_type")
		if !ok || target == "" {
			return nil, core.Invalid("modify_volume_type requires a non-empty volume_type parameter")
		}
		v.VolumeType = target
		if iops, ok := paramInt(params, "iops"); ok {
			v.IOPS = int64(iops)
		}
		if tp, ok := paramFloat(params, "throughput_mibps"); ok {
			v.ThroughputMiBps = tp
		}
		return map[string]any{
			"volume_type": v.VolumeType, "iops": v.IOPS, "throughput_mibps": v.ThroughputMiBps,
			"new_monthly_cost_micros": e.VolumeMonthlyCost(v).Micros(),
		}, nil
	},
	restore: func(e *Estate, id string, before map[string]any) error {
		v, ok := e.EBSVolumes[id]
		if !ok {
			return core.NotFound("ebs_volume", id)
		}
		t, ok := paramStr(before, "volume_type")
		if !ok || t == "" {
			return core.Invalid("rollback snapshot for %s is missing volume_type", id)
		}
		v.VolumeType = t
		if iops, ok := paramInt(before, "iops"); ok {
			v.IOPS = int64(iops)
		}
		if tp, ok := paramFloat(before, "throughput_mibps"); ok {
			v.ThroughputMiBps = tp
		}
		return nil
	},
}

// resizeVolumeSpec grows an EBS volume's size. AWS cannot shrink a volume
// in place — that always requires a new smaller volume and a data copy —
// so a target smaller than the current size is rejected outright rather
// than silently ignored.
var resizeVolumeSpec = actionSpec{
	action:          optimize.ActionResizeVolume,
	requiredActions: []string{"ec2:DescribeVolumes", "ec2:ModifyVolume"},
	kind:            cloud.KindEBSVolume,
	awsAction:       "ec2:ModifyVolume",
	titleFmt:        "Resize EBS volume %s",

	rollbackFeasible: true,
	dataLossRisk:     core.RiskNone,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		v, ok := e.EBSVolumes[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"size_gib": v.SizeGiB}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		v, ok := e.EBSVolumes[id]
		target, pok := paramFloat(params, "size_gib")
		return ok && pok && v.SizeGiB == target
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		v := e.EBSVolumes[id]
		target, ok := paramFloat(params, "size_gib")
		if !ok || target <= 0 {
			return nil, core.Invalid("resize_volume requires a positive size_gib parameter")
		}
		if target < v.SizeGiB {
			return nil, core.Invalid("EBS volumes cannot be shrunk (%.0f -> %.0f GiB); create a smaller replacement instead", v.SizeGiB, target)
		}
		v.SizeGiB = target
		return map[string]any{"size_gib": target, "new_monthly_cost_micros": e.VolumeMonthlyCost(v).Micros()}, nil
	},
	restore: func(e *Estate, id string, before map[string]any) error {
		v, ok := e.EBSVolumes[id]
		if !ok {
			return core.NotFound("ebs_volume", id)
		}
		sz, ok := paramFloat(before, "size_gib")
		if !ok {
			return core.Invalid("rollback snapshot for %s is missing size_gib", id)
		}
		v.SizeGiB = sz
		return nil
	},
}

// deleteSnapshotSpec permanently deletes an EBS snapshot. Like
// deleteVolumeSpec, this is irreversible: a deleted point-in-time backup
// cannot be un-deleted.
var deleteSnapshotSpec = actionSpec{
	action:          optimize.ActionDeleteSnapshot,
	requiredActions: []string{"ec2:DescribeSnapshots", "ec2:DeleteSnapshot"},
	kind:            cloud.KindEBSSnapshot,
	awsAction:       "ec2:DeleteSnapshot",
	titleFmt:        "Delete stale EBS snapshot %s",

	rollbackFeasible: false,
	infeasibleReason: "a deleted snapshot cannot be recovered",
	dataLossRisk:     core.RiskHigh,
	deleteAction:     true,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		s, ok := e.EBSSnapshots[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"size_gib": s.SizeGiB, "volume_id": s.VolumeID}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool { return false },
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		s := e.EBSSnapshots[id]
		freed := e.SnapshotMonthlyCost(s)
		delete(e.EBSSnapshots, id)
		return map[string]any{"deleted": true, "freed_monthly_cost_micros": freed.Micros()}, nil
	},
}

// releaseElasticIPSpec releases an unattached Elastic IP allocation.
// Releasing is marked irreversible not because the API call cannot be
// undone mechanically, but because re-allocating gets a different address
// — anything depending on the released IP (DNS, an allowlist) is broken
// regardless, matching AWS's own behaviour.
var releaseElasticIPSpec = actionSpec{
	action:          optimize.ActionReleaseElasticIP,
	requiredActions: []string{"ec2:DescribeAddresses", "ec2:ReleaseAddress"},
	kind:            cloud.KindElasticIP,
	awsAction:       "ec2:ReleaseAddress",
	titleFmt:        "Release unattached Elastic IP %s",

	rollbackFeasible: false,
	infeasibleReason: "a released Elastic IP's address cannot be reclaimed; a new allocation gets a different address",
	dataLossRisk:     core.RiskMedium,
	deleteAction:     true,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		ip, ok := e.ElasticIPs[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"public_ip": ip.PublicIP, "attached_to": ip.AttachedTo}, true
	},
	extraPrecondition: func(e *Estate, id string) error {
		ip, ok := e.ElasticIPs[id]
		if !ok {
			return nil
		}
		if ip.AttachedTo != "" {
			return core.Invalid("elastic IP %s is attached to %s; detach it before releasing", id, ip.AttachedTo)
		}
		return nil
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool { return false },
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		ip := e.ElasticIPs[id]
		freed := e.ElasticIPMonthlyCost(ip)
		delete(e.ElasticIPs, id)
		return map[string]any{"deleted": true, "freed_monthly_cost_micros": freed.Micros()}, nil
	},
}
