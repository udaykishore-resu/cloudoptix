package costing

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// fakeAWSSession implements ports.AWSSession by handing back a fixed
// aws.Config, standing in for a real sts.Session in every costing test in
// this package — see aws/sts.FromSession's own doc comment for why any
// ports.AWSSession works here, not just the concrete *sts.Session type.
type fakeAWSSession struct{ cfg aws.Config }

func (fakeAWSSession) AccountID() core.AccountID { return "222222222222" }
func (fakeAWSSession) Scope() cloud.RoleScope    { return cloud.ScopeAnalyze }
func (fakeAWSSession) ExpiresAt() time.Time      { return time.Now().Add(time.Hour) }
func (s fakeAWSSession) Config(core.Region) any  { return s.cfg }

func testSession() ports.AWSSession { return fakeAWSSession{cfg: aws.Config{Region: "us-east-1"}} }

func testAccount() cloud.AWSAccount {
	return cloud.AWSAccount{AccountID: "222222222222", TenantID: "tenant-1"}
}

func testAccountWithCUR() cloud.AWSAccount {
	a := testAccount()
	a.CURBucket = "cur-bucket"
	a.CURPrefix = "cur/cloudoptix-cur"
	return a
}
