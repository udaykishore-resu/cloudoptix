output "cur_bucket_name" {
  value = aws_s3_bucket.cur.id
}

output "cur_bucket_arn" {
  value = aws_s3_bucket.cur.arn
}

output "artefacts_bucket_name" {
  value = aws_s3_bucket.artefacts.id
}

output "artefacts_bucket_arn" {
  value = aws_s3_bucket.artefacts.arn
}

output "audit_bucket_name" {
  value = aws_s3_bucket.audit.id
}

output "audit_bucket_arn" {
  value = aws_s3_bucket.audit.arn
}
