## What

<!-- One or two sentences: what does this PR change, and why. -->

## Where

<!-- Check every area this PR touches. Helps a reviewer know which checklist below applies. -->

- [ ] `internal/` or other Go source
- [ ] `frontend/`
- [ ] `terraform/`
- [ ] `helm/`
- [ ] `deployments/` (Dockerfiles, compose, Argo CD, raw k8s manifests)
- [ ] `.github/` (CI/CD, dependabot)

## Terraform changes

<!-- Delete this section if this PR doesn't touch terraform/. -->

- [ ] `terraform fmt -recursive` run locally
- [ ] `terraform validate` passes for every module/environment touched
- [ ] No hardcoded account IDs, ARNs, or secrets — everything parameterised via variables
- [ ] Updated the module/environment `README.md` if a variable, default, or notable design choice changed
- [ ] If this changes a `production` or `staging` composition's cost-relevant resources: expect the `cost-regression` check to comment with a priced delta — read it before merging, not just its verdict

## Helm changes

<!-- Delete this section if this PR doesn't touch helm/. -->

- [ ] `helm lint helm/cloudoptix -f helm/cloudoptix/values.yaml -f <env values>` passes
- [ ] `helm template` renders valid YAML for both `values-dev.yaml` and `values-production.yaml` overlays
- [ ] Every new value is documented in `helm/cloudoptix/README.md`
- [ ] Resource requests/limits are honest numbers, not placeholders

## Security

- [ ] No secret, token, or credential is committed (checked, not just assumed — `gitleaks` will re-check in CI)
- [ ] New IAM/RBAC grants are the minimum the change needs, and are explained in a comment citing what requires them

## How this was tested

<!-- What you ran, and what you saw. "CI is green" is necessary, not sufficient — say what you did locally too, if anything. -->
