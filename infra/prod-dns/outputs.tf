output "zone_ids" {
  value = { for k, z in aws_route53_zone.delegated : z.name => z.zone_id }
}

# Add exactly these at GoDaddy (DNS, Add New Record, type NS): Name is the
# label, Value is the name server. Four per label, twelve in all. Add nothing
# else and change nothing that is already there.
output "godaddy_ns_records" {
  description = "NS records to add at GoDaddy for the delegation."
  value = flatten([
    for label in sort(tolist(var.delegated_subdomains)) : [
      for ns in sort(aws_route53_zone.delegated[label].name_servers) : {
        type  = "NS"
        name  = label
        value = ns
        ttl   = "1 hour"
      }
    ]
  ])
}
