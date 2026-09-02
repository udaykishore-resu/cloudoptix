package executor

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// fakeAWSSession implements ports.AWSSession the same way every other
// aws/* package's tests do (see aws/sts.FromSession's own doc comment for
// why any ports.AWSSession works, not just the concrete *sts.Session type).
type fakeAWSSession struct{ cfg aws.Config }

func (fakeAWSSession) AccountID() core.AccountID { return "222222222222" }
func (fakeAWSSession) Scope() cloud.RoleScope    { return cloud.ScopeExecute }
func (fakeAWSSession) ExpiresAt() time.Time      { return time.Now().Add(time.Hour) }
func (s fakeAWSSession) Config(core.Region) any  { return s.cfg }

func testSession() ports.AWSSession { return fakeAWSSession{cfg: aws.Config{Region: "us-east-1"}} }

func testResource(kind cloud.Kind, nativeID string) cloud.Resource {
	return cloud.Resource{ID: "res-1", Kind: kind, NativeID: nativeID, Region: "us-east-1", ARN: core.ARN("arn:aws:x:us-east-1:222222222222:x/" + nativeID)}
}

func testRecommendation(action optimize.ActionType, params map[string]any) optimize.Recommendation {
	return optimize.Recommendation{ID: "rec-1", Action: action, Parameters: params, EstimatedMonthlySaving: core.NewMoney(10, core.USD)}
}

func testPlanInput(action optimize.ActionType, r cloud.Resource, params map[string]any) ports.ExecutionPlanInput {
	return ports.ExecutionPlanInput{
		TenantID: "tenant-1", Recommendation: testRecommendation(action, params), Resource: r,
		Account: cloud.AWSAccount{AccountID: "222222222222"}, Session: testSession(), RequestedBy: "test",
	}
}
