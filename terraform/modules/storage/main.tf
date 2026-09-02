locals {
  common_tags = merge(var.tags, {
    Module      = "storage"
    Environment = var.environment
    ManagedBy   = "terraform"
  })
}

data "aws_caller_identity" "current" {}

# ---------------------------------------------------------------------------
# CUR ingestion bucket — the raw Cost and Usage Report export a customer's
# billing account delivers to. internal/application/costing reads from here
# to derive cost.* domain records; the bucket itself is a landing zone, not
# the retained record.
# ---------------------------------------------------------------------------

resource "aws_s3_bucket" "cur" {
  bucket = "${var.name}-cur-ingestion"
  tags   = merge(local.common_tags, { Purpose = "cur-ingestion" })
}

resource "aws_s3_bucket_versioning" "cur" {
  bucket = aws_s3_bucket.cur.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "cur" {
  bucket = aws_s3_bucket.cur.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = var.app_kms_key_arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "cur" {
  bucket                  = aws_s3_bucket.cur.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "cur" {
  bucket = aws_s3_bucket.cur.id
  rule {
    id     = "transition-then-expire"
    status = "Enabled"
    transition {
      days          = var.cur_lifecycle_transition_days
      storage_class = "GLACIER_IR"
    }
    expiration {
      days = var.cur_expiration_days
    }
    noncurrent_version_expiration {
      noncurrent_days = 30
    }
    # Clean up the multipart-upload waste this module's own README calls out
    # in terraform/demo's "bucket with no lifecycle policy" pathology — the
    # CUR bucket receives large scheduled billing exports, so an abandoned
    # multipart upload here is a realistic, not hypothetical, cost leak.
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

# ---------------------------------------------------------------------------
# Artefacts bucket — execution plan snapshots, generated reports, exported
# recommendation sets: anything internal/application writes as a durable
# blob rather than a database row.
# ---------------------------------------------------------------------------

resource "aws_s3_bucket" "artefacts" {
  bucket = "${var.name}-artefacts"
  tags   = merge(local.common_tags, { Purpose = "artefacts" })
}

resource "aws_s3_bucket_versioning" "artefacts" {
  bucket = aws_s3_bucket.artefacts.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "artefacts" {
  bucket = aws_s3_bucket.artefacts.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = var.app_kms_key_arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "artefacts" {
  bucket                  = aws_s3_bucket.artefacts.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "artefacts" {
  bucket = aws_s3_bucket.artefacts.id
  rule {
    id     = "expire-noncurrent"
    status = "Enabled"
    noncurrent_version_expiration {
      noncurrent_days = var.artefacts_noncurrent_expiration_days
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

# ---------------------------------------------------------------------------
# Audit archive bucket — the retained record behind internal/domain/audit.
# Object lock in COMPLIANCE mode: see the audit_object_lock_retention_days
# variable doc for why this is a stronger guarantee than an IAM policy.
#
# Object lock can only be enabled at bucket *creation*, which is why
# object_lock_enabled is set on the bucket resource itself rather than a
# separate resource — there is no way to retrofit this onto an existing
# bucket, so a customer/environment that skipped it here cannot add it
# later without a bucket migration.
# ---------------------------------------------------------------------------

resource "aws_s3_bucket" "audit" {
  bucket              = "${var.name}-audit-archive"
  object_lock_enabled = true
  tags                = merge(local.common_tags, { Purpose = "audit-archive" })
}

# Versioning is a prerequisite for object lock (AWS enforces this) — every
# retained object is necessarily also a specific version.
resource "aws_s3_bucket_versioning" "audit" {
  bucket = aws_s3_bucket.audit.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_object_lock_configuration" "audit" {
  bucket = aws_s3_bucket.audit.id
  rule {
    default_retention {
      mode = "COMPLIANCE"
      days = var.audit_object_lock_retention_days
    }
  }
  depends_on = [aws_s3_bucket_versioning.audit]
}

resource "aws_s3_bucket_server_side_encryption_configuration" "audit" {
  bucket = aws_s3_bucket.audit.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = var.audit_kms_key_arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "audit" {
  bucket                  = aws_s3_bucket.audit.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# No expiration/transition lifecycle rule on the audit bucket by design —
# transitioning a COMPLIANCE-locked object to a cheaper storage class is
# fine and safe to add later, but this module deliberately does not assume
# a storage-class trade-off on the customer's behalf for their compliance
# archive; see the README.
