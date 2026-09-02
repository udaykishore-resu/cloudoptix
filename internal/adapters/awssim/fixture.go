package awssim

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed fixture.yaml
var fixtureYAML []byte

// fixtureAccount is the top-level account identity block.
type fixtureAccount struct {
	AccountID string `yaml:"account_id"`
	Alias     string `yaml:"alias"`
	Region    string `yaml:"region"`
}

type fixtureSubnet struct {
	ID     string `yaml:"id"`
	Name   string `yaml:"name"`
	AZ     string `yaml:"az"`
	CIDR   string `yaml:"cidr"`
	Public bool   `yaml:"public"`
}

type fixtureSecurityGroup struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
}

type fixtureVPC struct {
	ID             string                 `yaml:"id"`
	Name           string                 `yaml:"name"`
	CIDR           string                 `yaml:"cidr"`
	Subnets        []fixtureSubnet        `yaml:"subnets"`
	SecurityGroups []fixtureSecurityGroup `yaml:"security_groups"`
}

type fixtureNATGateway struct {
	ID               string  `yaml:"id"`
	Name             string  `yaml:"name"`
	AZ               string  `yaml:"az"`
	Subnet           string  `yaml:"subnet"`
	GBProcessedMonth float64 `yaml:"gb_processed_month"`
}

// fixtureParams are the tunable counts, ranges and ratios that the bulk
// generators in demo.go expand. Keeping them as plain float64/int fields
// (rather than a nested structure) is deliberate: every knob that shapes the
// estate's total cost or waste envelope is visible in one flat YAML block,
// which is what makes re-tuning the target range a data change, not a code
// change.
type fixtureParams struct {
	Seed int64 `yaml:"seed"`

	EKSNodeGroupASize         int     `yaml:"eks_nodegroup_a_size"`
	EKSNodeGroupBSize         int     `yaml:"eks_nodegroup_b_size"`
	EKSPackedFraction         float64 `yaml:"eks_packed_fraction"`
	EKSRequestOverActualRatio float64 `yaml:"eks_request_over_actual_ratio"`

	FargateServiceCount    int `yaml:"fargate_service_count"`
	FargateAvgDesiredCount int `yaml:"fargate_avg_desired_count"`
	FargateAvgCPUUnits     int `yaml:"fargate_avg_cpu_units"`
	FargateAvgMemoryMB     int `yaml:"fargate_avg_memory_mb"`

	UnattachedGP2Count   int `yaml:"unattached_gp2_count"`
	UnattachedGP2MinGiB  int `yaml:"unattached_gp2_min_gib"`
	UnattachedGP2MaxGiB  int `yaml:"unattached_gp2_max_gib"`
	GP2ShouldBeGP3Count  int `yaml:"gp2_should_be_gp3_count"`
	GP2ShouldBeGP3MinGiB int `yaml:"gp2_should_be_gp3_min_gib"`
	GP2ShouldBeGP3MaxGiB int `yaml:"gp2_should_be_gp3_max_gib"`

	SnapshotCount            int `yaml:"snapshot_count"`
	SnapshotMinGiB           int `yaml:"snapshot_min_gib"`
	SnapshotMaxGiB           int `yaml:"snapshot_max_gib"`
	SnapshotMinAgeDays       int `yaml:"snapshot_min_age_days"`
	SnapshotMaxAgeDays       int `yaml:"snapshot_max_age_days"`
	SnapshotOldThresholdDays int `yaml:"snapshot_old_threshold_days"`

	UnattachedEIPCount int `yaml:"unattached_eip_count"`
	AttachedEIPCount   int `yaml:"attached_eip_count"`

	LogGroupCount               int     `yaml:"log_group_count"`
	LogGroupNeverExpireFraction float64 `yaml:"log_group_never_expire_fraction"`

	UntaggedFraction float64 `yaml:"untagged_fraction"`
}

// fixture is the full on-disk shape of fixture.yaml.
type fixture struct {
	Account     fixtureAccount      `yaml:"account"`
	VPC         fixtureVPC          `yaml:"vpc"`
	NATGateways []fixtureNATGateway `yaml:"nat_gateways"`
	Params      fixtureParams       `yaml:"params"`
}

// loadFixture parses the embedded fixture. It panics on malformed embedded
// YAML for the same reason pricing.New does: the file is authored and
// committed by this package, and a parse failure here is a build-time
// defect, not a runtime condition callers can recover from.
func loadFixture() fixture {
	var f fixture
	if err := yaml.Unmarshal(fixtureYAML, &f); err != nil {
		panic(fmt.Sprintf("awssim: embedded fixture.yaml is malformed: %v", err))
	}
	return f
}
