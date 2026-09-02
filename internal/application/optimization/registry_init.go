package optimization

import (
	"log/slog"

	rulepack "github.com/udaykishore-resu/cloudoptix/rules"
)

// NewDefaultRegistry loads the embedded YAML rule pack and registers every
// rule this package ships, in a fixed order (grouped by category, matching
// the YAML pack's own file layout). Registration order has no effect on
// evaluation order — Registry.Evaluate always sorts by rule ID — but keeping
// it grouped here is what makes "did I register every rule in the pack"
// reviewable at a glance against rules/README.md's category list.
//
// Traceability: REQ-OPT-001.
func NewDefaultRegistry(logger *slog.Logger) (*Registry, error) {
	pack, err := rulepack.Load()
	if err != nil {
		return nil, err
	}
	reg := NewRegistry(pack, logger)

	// Compute / EC2.
	reg.Register(NewEC2RightsizeRule())
	reg.Register(NewEC2OversizedDeclaredRule())
	reg.Register(NewEC2StoppedStorageRule())
	reg.Register(NewEC2NeverUsedRule())
	reg.Register(NewEC2PrevGenerationRule())
	reg.Register(NewEC2BurstCreditRule())
	reg.Register(NewEC2ScheduleOffHoursRule())

	// Storage / EBS / S3 / AMI.
	reg.Register(NewEBSUnattachedRule())
	reg.Register(NewEBSOverprovisionedRule())
	reg.Register(NewEBSGp2Gp3Rule())
	reg.Register(NewEBSSnapshotRetentionRule())
	reg.Register(NewEBSOrphanedSnapshotRule())
	reg.Register(NewEBSUnusedAMIRule())
	reg.Register(NewS3NoLifecycleRule())
	reg.Register(NewS3WrongStorageClassRule())
	reg.Register(NewS3NoncurrentVersionsRule())
	reg.Register(NewS3IncompleteMultipartRule())
	reg.Register(NewS3IntelligentTieringRule())

	// Database / RDS / DynamoDB.
	reg.Register(NewRDSOversizedRule())
	reg.Register(NewRDSIdleRule())
	reg.Register(NewRDSOverprovisionedStorageRule())
	reg.Register(NewRDSUnnecessaryReplicaRule())
	reg.Register(NewRDSMultiAZNonProdRule())
	reg.Register(NewRDSGp2Gp3Rule())
	reg.Register(NewRDSBackupRetentionRule())
	reg.Register(NewRDSAuroraCandidacyRule())

	// Network.
	reg.Register(NewNATVPCEndpointRule())
	reg.Register(NewNATRedundantRule())
	reg.Register(NewCrossAZChatterRule())
	reg.Register(NewEIPUnattachedRule())
	reg.Register(NewLBIdleRule())
	reg.Register(NewCloudFrontEgressRule())

	// Serverless / Lambda.
	reg.Register(NewLambdaMemoryCostCurveRule())
	reg.Register(NewLambdaUnusedProvisionedConcurrencyRule())
	reg.Register(NewLambdaGravitonRule())
	reg.Register(NewLambdaExcessiveTimeoutRule())

	// Kubernetes / EKS / ECS.
	reg.Register(NewEKSNodeGroupOverprovisionedRule())
	reg.Register(NewK8sPodRequestsOversizedRule())
	reg.Register(NewEKSNodeGroupNoSpotRule())
	reg.Register(NewEKSConsolidationRule())
	reg.Register(NewFargateVsEC2Rule())
	reg.Register(NewECSTaskCountRule())

	// Observability.
	reg.Register(NewCWLogRetentionRule())
	reg.Register(NewCWHighCardinalityRule())
	reg.Register(NewKMSSecretsUnusedRule())

	// Commitment / purchase model.
	reg.Register(NewEC2SpotCandidacyRule())
	reg.Register(NewEC2CommitmentGapRule())
	reg.Register(NewDynamoBillingModeRule())

	return reg, nil
}
