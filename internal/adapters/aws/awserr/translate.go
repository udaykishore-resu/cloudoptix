// Package awserr translates AWS SDK v2 errors into CloudOptix's own sentinel
// errors.
//
// Every adapter under internal/adapters/aws talks to a different service
// client, but all of them fail in the same three shapes that the rest of the
// platform needs to react to uniformly: the call was throttled (back off and
// retry), the caller lacks an IAM permission (surface exactly which action so
// the customer's policy can be fixed), or something else went wrong (bubble
// up as-is). Putting that translation in one place, keyed off the smithy
// APIError interface every generated client returns rather than off each
// service's bespoke exception types, is what lets twenty-plus discoverers and
// a dozen executors share one error-classification decision instead of
// reimplementing (and inevitably disagreeing on) it twenty times.
//
// Traceability: REQ-DSC-002, REQ-SEC-001, SPEC-DSC-001.
package awserr

import (
	"errors"
	"regexp"
	"strings"

	"github.com/aws/smithy-go"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// throttleCodes lists the ErrorCode() values AWS services use for rate
// limiting. There is no single shared exception type across services — EC2
// says RequestLimitExceeded, DynamoDB says ProvisionedThroughputExceededException,
// API Gateway says TooManyRequestsException — so the set is enumerated rather
// than pattern-matched, and new codes are added here as they are met in
// production rather than guessed at speculatively.
var throttleCodes = map[string]bool{
	"Throttling":                             true,
	"ThrottlingException":                    true,
	"ThrottledException":                     true,
	"RequestLimitExceeded":                   true,
	"TooManyRequestsException":               true,
	"ProvisionedThroughputExceededException": true,
	"RequestThrottledException":              true,
	"SlowDown":                               true,
	"LimitExceededException":                 true,
	"Ec2ThrottledException":                  true,
	"BandwidthLimitExceeded":                 true,
	"PriorRequestNotComplete":                true,
}

// deniedCodes lists the ErrorCode() values that mean an IAM principal lacks a
// permission, as opposed to the request itself being malformed.
var deniedCodes = map[string]bool{
	"AccessDenied":                true,
	"AccessDeniedException":       true,
	"UnauthorizedOperation":       true,
	"UnauthorizedException":       true,
	"AuthFailure":                 true,
	"NotAuthorizedException":      true,
	"AuthorizationErrorException": true,
}

// APIErrorOf extracts the smithy APIError from an error chain, matching the
// same errors.As pattern every AWS SDK v2 caller is expected to use rather
// than a service-specific concrete type assertion, which would tie this
// package to twenty-plus import graphs.
func APIErrorOf(err error) (smithy.APIError, bool) {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// Throttled reports whether err represents a rate-limit response.
func Throttled(err error) bool {
	apiErr, ok := APIErrorOf(err)
	if !ok {
		return false
	}
	return throttleCodes[apiErr.ErrorCode()]
}

// AccessDenied reports whether err represents a missing-permission response.
func AccessDenied(err error) bool {
	apiErr, ok := APIErrorOf(err)
	if !ok {
		return false
	}
	return deniedCodes[apiErr.ErrorCode()]
}

// deniedActionPattern matches the IAM action AWS embeds in an AccessDenied
// message, e.g. `User: arn:aws:iam::111111111111:role/x is not authorized to
// perform: ec2:DescribeInstances on resource: ...`. Every service that denies
// with an explicit action name uses this phrasing; when a message does not
// (some older services just say "Access Denied" with no detail) the caller's
// fallback action name is used instead.
var deniedActionPattern = regexp.MustCompile(`(?i)not authorized to perform:\s*([A-Za-z0-9]+:[A-Za-z0-9*]+)`)

// DeniedAction extracts the specific IAM action a denial names, falling back
// to fallback (typically the action the caller was attempting) when AWS's
// message does not name one. The discovery and execution orchestrators both
// depend on getting a real action name here — "access denied" with no detail
// is not actionable, but "ec2:DescribeNatGateways was denied" tells the
// customer exactly what to add to the read role's policy.
func DeniedAction(err error, fallback string) string {
	apiErr, ok := APIErrorOf(err)
	if !ok {
		return fallback
	}
	if m := deniedActionPattern.FindStringSubmatch(apiErr.ErrorMessage()); len(m) == 2 {
		return m[1]
	}
	return fallback
}

// Translate maps err onto CloudOptix's sentinel errors. action names the IAM
// action the caller was attempting, used to build an actionable Forbidden
// error and to fall back to when AWS's own denial message does not name one.
// service/op identify the call for the wrapped message; a nil err returns
// nil.
func Translate(err error, service, op, action string) error {
	if err == nil {
		return nil
	}
	switch {
	case Throttled(err):
		return core.NewError(core.ErrThrottled, "aws_throttled", "%s:%s was throttled", service, op).
			WithDetail("service", service).WithDetail("operation", op).Wrap(err)
	case AccessDenied(err):
		denied := DeniedAction(err, action)
		return core.Forbidden("%s:%s denied: missing IAM permission %s", service, op, denied).
			WithDetail("action", denied).WithDetail("service", service).WithDetail("operation", op).Wrap(err)
	default:
		return err
	}
}

// ServiceUnavailable reports whether err means the service is not offered in
// the region that was queried (as opposed to any other failure), which a
// per-region discoverer must treat as "skip this region" rather than a hard
// failure — many AWS services are regional opt-in and a customer's account
// simply may not have, say, EKS enabled in eu-north-1.
func ServiceUnavailable(err error) bool {
	apiErr, ok := APIErrorOf(err)
	if !ok {
		return false
	}
	code := apiErr.ErrorCode()
	if code == "UnrecognizedClientException" || code == "InvalidClientTokenId" {
		return false // credentials problem, not a region-availability problem
	}
	msg := strings.ToLower(apiErr.ErrorMessage())
	return strings.Contains(msg, "could not be found") && strings.Contains(msg, "endpoint") ||
		strings.Contains(msg, "not supported in this region") ||
		strings.Contains(msg, "opt-in required")
}
