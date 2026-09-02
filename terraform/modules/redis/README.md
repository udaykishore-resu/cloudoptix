# redis

ElastiCache Redis, encrypted in transit and at rest, backing
`internal/infrastructure/config.RedisConfig` (shared cache + distributed
lock backend — see `CLOUDOPTIX_REDIS_*`).

## Encryption is not optional here

`at_rest_encryption_enabled` and `transit_encryption_enabled` are both
hard-coded `true`, not exposed as variables. ElastiCache is where
CloudOptix's own distributed locks and cached AWS API responses live for a
customer's account — nothing here is disposable-if-leaked in the way, say,
a public CDN cache might be, and there is no environment (including dev)
where turning this off is the right trade-off. `helm/cloudoptix`'s Redis
client config expects TLS accordingly (`redis.tls_enabled` in
`internal/infrastructure/config`).

## AUTH token generation

ElastiCache, unlike RDS, has no "let AWS manage this credential for you"
option (`manage_master_user_password` is an RDS/Aurora-only feature). This
module generates the token itself with `random_password` and writes it into
the Secrets Manager container the security module created
(`var.secret_arn`, entry `redis-password`). The token exists in two places:
Terraform's own remote state (S3, encrypted — see
`terraform/environments/*/backend.tf`) and that Secrets Manager version. It
is never written into any committed file, and the module's outputs expose
only the secret's ARN, never its value — sync it into the running
application the same way as the RDS password, via ExternalSecrets into
`CLOUDOPTIX_REDIS_PASSWORD`.

## Sizing

`cache.t4g.small` / a single node is the dev default. Production should be
sized from `cloudoptix_cache_hits_total` / `cloudoptix_cache_misses_total`
(see `internal/infrastructure/telemetry/metrics.go`) and observed memory
pressure — not guessed. `num_cache_clusters >= 2` with
`automatic_failover_enabled = true` is the production-appropriate setting;
the environment compositions under `terraform/environments/production` pin
this.
