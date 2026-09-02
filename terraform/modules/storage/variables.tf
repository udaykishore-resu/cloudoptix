variable "name" {
  type = string
}

variable "environment" {
  type = string
}

variable "app_kms_key_arn" {
  description = "KMS key (from the security module) encrypting the CUR-ingestion and artefacts buckets."
  type        = string
}

variable "audit_kms_key_arn" {
  description = "KMS key (from the security module) encrypting the audit archive bucket."
  type        = string
}

variable "audit_object_lock_retention_days" {
  description = <<-EOT
    COMPLIANCE-mode object lock retention on the audit archive bucket, in
    days. Default is 7 years (2557 days), matching the longer end of typical
    SOX/financial-controls retention expectations for change-control records.

    Why object lock at all: internal/domain/audit is CloudOptix's record of
    every recommendation, approval, and execution the platform ever took (or
    proposed) on a customer's infrastructure — the artifact a customer's
    security or compliance reviewer reaches for after an incident, and the
    thing that proves an autonomous execution was in fact policy-approved
    before it ran. COMPLIANCE mode means *no principal, including the
    account root user*, can shorten a retained object's retention period or
    delete it early. That is a deliberately stronger guarantee than IAM
    alone can give: an IAM policy is only as strong as nobody with
    sufficient privilege (or a compromised credential with sufficient
    privilege) ever changing it. Object lock in COMPLIANCE mode removes "an
    admin credential is compromised" from the set of ways this bucket's
    retention guarantee can fail.
  EOT
  type = number
  default = 2557
}

variable "cur_lifecycle_transition_days" {
  description = "Days before a CUR ingestion object transitions to Glacier Instant Retrieval — CUR files are read repeatedly during the retention window this app actually processes them (a rolling ~13 months) but are cheap to keep further back for reprocessing/audit."
  type        = number
  default     = 90
}

variable "cur_expiration_days" {
  description = "Days before a CUR ingestion object is deleted outright. CUR data is a raw AWS billing export CloudOptix re-derives its own cost.* domain records from — it is not the retained record itself (audit is), so it does not need indefinite retention."
  type        = number
  default     = 400
}

variable "artefacts_noncurrent_expiration_days" {
  description = "Days a non-current artefact version is kept before expiring, once versioning is enabled."
  type        = number
  default     = 90
}

variable "tags" {
  type    = map(string)
  default = {}
}
