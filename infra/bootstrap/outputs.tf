output "state_bucket" {
  description = "Pass this as -backend-config=\"bucket=...\" when initialising the other roots."
  value       = aws_s3_bucket.state.bucket
}
