// Package awsaccounts implements ports.AWSAccountService: onboarding and
// verifying the customer AWS accounts CloudOptix analyses and, when
// automation is enabled, changes.
//
// KEY DESIGN DECISION: an account cannot be registered until
// tenancy.Tenant.CanConnectAWS says so, and it cannot be registered in
// simulated mode unless the tenant is the demo tenant. Both gates are
// enforced here rather than trusted from the caller, because the whole
// spec-driven promise of the platform — "nothing runs against your
// infrastructure that was not reviewed in the specification you approved"
// — is only as strong as its weakest enforcement point. A UI bug or a
// scripted API call that skips the review step must fail here, not merely
// fail to be offered a button for.
//
// Registration never accepts long-lived AWS credentials — see
// ports.AWSCredentialBroker's own doc comment — and always mints a fresh,
// cryptographically random external id per account: reusing one across
// accounts, or letting a customer choose their own, would weaken the
// confused-deputy defence the external id exists to provide.
//
// Traceability: REQ-SEC-001, SPEC-SEC-001, SPEC-ONB-001.
package awsaccounts
