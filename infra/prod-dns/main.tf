# Prod DNS for mailgoose.app, without moving the domain.
#
# mailgoose.app stays at GoDaddy: it carries live Microsoft 365 mail and the
# website. Each name prod needs is delegated to its own Route 53 zone, the same
# way staging.mailgoose.app already is, by adding NS records for that one name at
# GoDaddy. Nothing at the apex, www, autodiscover or any other existing record
# changes, because a delegation only takes over the name it is added at and
# everything beneath it.
#
#   api.mailgoose.app   the REST API (ALB alias, ACM validation record)
#   app.mailgoose.app   the web app (ALB alias, ACM validation record)
#   mail.mailgoose.app  mailgoose's own SES sending domain: Easy DKIM CNAMEs,
#                       bounce.mail (custom MAIL FROM) and _dmarc.mail
#
# This root only creates the zones. envs/prod looks them up by name and adds the
# records. Apply order: this root, then the NS records at GoDaddy
# (terraform output godaddy_ns_records), then envs/prod. envs/prod's certificate
# validation waits for the delegation to resolve.

resource "aws_route53_zone" "delegated" {
  for_each = var.delegated_subdomains

  name    = "${each.key}.${var.domain}"
  comment = "Delegated from GoDaddy; the rest of ${var.domain} is not hosted in Route 53."

  # Deleting a zone and creating it again gives it new name servers, which would
  # silently break the delegation at GoDaddy.
  lifecycle {
    prevent_destroy = true
  }
}
