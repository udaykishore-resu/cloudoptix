// Package sts is CloudOptix's only path onto a customer's AWS account.
//
// The key design decision is structural, not procedural: this package has no
// function, no constructor option and no struct field anywhere that accepts
// an AWS access key ID or secret access key. Broker is built from an
// aws.Config whose Credentials this package never sets from a literal — the
// caller obtains that base config however its own runtime provides identity
// (an ECS task role, an EC2 instance profile, a local SSO profile in
// development), CloudOptix's own control-plane identity, and every credential
// this package hands to a caller afterwards is minted by sts:AssumeRole
// against that base identity, never accepted as input. A reviewer can
// therefore verify "no static keys ever touch a customer account" by grepping
// this package for AccessKeyId/SecretAccessKey as an input parameter and
// finding none — it is not a policy CloudOptix promises to follow, it is a
// shape the code cannot express.
//
// Every assumed session is scoped to exactly one (account, cloud.RoleScope)
// pair — Read, Analyze, Plan or Execute — because those are four separate IAM
// roles in the customer's account, not four permission checks against one
// wide role. A tenant that never creates the execute role simply has no way
// to obtain an execute-scoped session; Broker.Assume returns an error the
// same way AssumeRole itself would if the role did not exist.
//
// Every AssumeRole call carries the account's ExternalID (the confused-deputy
// defence: without it, a customer's trust policy could be assumed by any
// CloudOptix tenant, not just the one it was written for) and a
// RoleSessionName built from the CloudOptix principal and the requesting
// scope, so the customer's own CloudTrail attributes every action CloudOptix
// took to a specific session, not to an anonymous "AssumeRole" line.
//
// Sessions are cached per (account, scope) and refreshed proactively — a
// caller never observes an about-to-expire credential — and concurrent
// refreshes for the same key coalesce onto one AssumeRole call rather than
// stampeding STS when many goroutines discover the same expired session at
// once (Broker.Assume serializes refresh through a per-key mutex, and the
// aws.CredentialsCache wrapping every minted session's provider adds a second
// layer of coalescing at the point credentials are actually used to sign a
// request, independent of whether the caller ever calls Assume again).
//
// Traceability: REQ-SEC-001, SPEC-SEC-001.
package sts
