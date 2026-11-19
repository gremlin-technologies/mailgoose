output "bucket_name" {
  value = aws_s3_bucket.this.id
}

output "bucket_arn" {
  value = aws_s3_bucket.this.arn
}

output "base_url" {
  description = "Where uploaded images are served from (IMAGES_BASE_URL): the CloudFront address, or null without the CDN, when the API serves them at its own /images/."
  value       = var.cdn ? "https://${aws_cloudfront_distribution.this[0].domain_name}" : null
}

output "distribution_id" {
  value = var.cdn ? aws_cloudfront_distribution.this[0].id : null
}
