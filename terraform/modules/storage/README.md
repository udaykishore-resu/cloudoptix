# storage

Three S3 buckets, each with a distinct retention story:

| Bucket | Versioning | Encryption | Retention |
|---|---|---|---|
| `<name>-cur-ingestion` | on | `app` KMS key | lifecycle: Glacier IR at 90d, expire at 400d |
| `<name>-artefacts` | on | `app` KMS key | expire non-current versions at 90d |
| `<name>-audit-archive` | on (required) | `audit` KMS key | **object lock, COMPLIANCE mode, 7y default** |

All three block every form of public access unconditionally.

## Why the audit bucket is different

`internal/domain/audit` is CloudOptix's record of every recommendation,
approval and execution the platform ever proposed or performed against a
customer's infrastructure. It is the artifact a customer's security
reviewer pulls after an incident, and the proof that an autonomous
execution (`features.autonomous_execution` in
`internal/infrastructure/config`) was actually policy-approved before it
ran — not an after-the-fact claim.

An IAM policy that says "nobody may delete this" is only as strong as
nobody with sufficient privilege — or a compromised credential with
sufficient privilege — ever changing that policy. S3 Object Lock in
**COMPLIANCE** mode is a different, stronger kind of guarantee: once an
object is under COMPLIANCE retention, *no principal, including the AWS
account root user*, can shorten its retention period or delete it before
the retention date, full stop. That property is what makes this bucket
useful as evidence rather than merely as a backup.

Two consequences worth knowing before touching this module:

- **Object lock can only be enabled at bucket creation.** There is no
  Terraform (or console, or CLI) path to retrofit it onto an existing
  bucket. If an environment's audit bucket was ever created without it,
  the fix is a new bucket and a data migration, not a config change.
- **COMPLIANCE mode cannot be relaxed, including by Terraform.** If you set
  `audit_object_lock_retention_days` too high for an environment (e.g. a
  disposable dev/demo account), you cannot shorten it later or destroy
  those objects before the retention date. `terraform/demo` does not use
  this module for exactly that reason — see its README.

## Lifecycle policy is a first-class input, not an afterthought

The CUR bucket receives large, regularly-scheduled billing export files —
exactly the shape of bucket where an abandoned multipart upload is a real
cost leak, not a hypothetical one, which is why both non-audit buckets
carry `abort_incomplete_multipart_upload`. `terraform/demo` provisions a
bucket that deliberately omits all of this, as the pathology CloudOptix's
`apply_s3_lifecycle` and `abort_multipart_uploads` executor actions
(`internal/adapters/aws/executor/s3.go`) exist to fix.
