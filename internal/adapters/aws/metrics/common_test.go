package metrics

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// fakeAWSSession implements ports.AWSSession by handing back a fixed
// aws.Config — see aws/sts.FromSession's own doc comment for why any
// ports.AWSSession works here, not just the concrete *sts.Session type.
type fakeAWSSession struct{ cfg aws.Config }

func (fakeAWSSession) AccountID() core.AccountID { return "222222222222" }
func (fakeAWSSession) Scope() cloud.RoleScope    { return cloud.ScopeAnalyze }
func (fakeAWSSession) ExpiresAt() time.Time      { return time.Now().Add(time.Hour) }
func (s fakeAWSSession) Config(core.Region) any  { return s.cfg }

func testSession() ports.AWSSession { return fakeAWSSession{cfg: aws.Config{Region: "us-east-1"}} }

func testWindow() core.Period {
	end := time.Now().UTC()
	return core.NewPeriod(end.Add(-time.Hour), end)
}
