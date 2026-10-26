# tagherd

tagherd scans AWS, Azure and GCP resources against a tagging policy, writes a
reviewable plan of tag changes, and applies it. `scan` reads, `plan` decides,
`apply` writes: nothing changes in a cloud account unless `apply` runs.

- Spec: [spec/tagherd CLI Spec.md](spec/tagherd%20CLI%20Spec.md)
- Component designs and backlog: linked at the end of the spec

## Status

All seven phases are implemented:

- Config, policy loading and validation, and the type catalog
- The evaluation engine: classification, group matching, every finding code,
  compliance summaries and `--fail-on` exit codes
- Discovery adapters for AWS (Organizations, AWS Config, per-service list
  calls), Azure (Resource Graph) and GCP (Cloud Asset Inventory, labels and
  Resource Manager tags), behind `scan` and `inventory`
- `plan`: values from `--set`, `defaults.yaml`, parent tags and the built-in
  rules (`ownership.yaml`, `synonyms.yaml`), the write route by
  DeploymentType, the grace period, and the plan file
- `apply` for the direct route: plan checks, confirmation, preconditions,
  provider limits, tag writers for AWS, Azure and GCP, the audit log and
  `--verify`
- `apply` for the Terraform route: repository allowlist, state lookup, HCL
  edits, one patch per repository, and pull requests on GitHub and
  Bitbucket Cloud with `--pr`
- The Wiz override for machine images: the AMI group's graph query runs
  through the Wiz API, with a safe fallback when Wiz is not available

The adapters are unit-tested against stubs, and the Terraform route against
local git repositories; none has yet run against real accounts or SCMs (the
pilot in the backlog). The same goes for the Wiz client, which is tested
against a stub server.

## Build and test

Requires Go 1.26 or later.

```bash
make build      # bin/tagherd
make test       # go test -race ./...
make validate   # build, then validate ./policy
```

## Usage

```bash
tagherd policy validate                 # check ./policy; exit 2 on errors
tagherd policy validate --strict        # warnings fail too
tagherd policy list                     # groups with tag and type counts
tagherd policy show database            # by group name or file stem; globs allowed
tagherd catalog --cloud aws --type '*rds*'
tagherd catalog --gaps                  # types with no discovery adapter yet
tagherd init                            # tagherd.yaml, value files, policy scaffold

# Scan the clouds configured under scopes: in tagherd.yaml
tagherd scan --cloud aws --account '1111*' --group Database
tagherd scan --fail-on missing --summary -o markdown
tagherd inventory --cloud gcp --type 'GCP Compute*' -o csv

# Write plan.json (mode 0600) and print it; nothing is written to a cloud
tagherd plan --set Brand=Acme --defaults defaults.yaml --out plan.json
tagherd plan --grace 30m -o markdown

# Write the plan's direct changes and Terraform patches; every attempt goes
# to tagherd-audit.ndjson
tagherd apply --plan plan.json            # asks for confirmation
tagherd apply --plan plan.json --yes --verify -o markdown
tagherd apply --plan plan.json --only terraform --patch-dir patches
tagherd apply --plan plan.json --only terraform --pr

# The fake-provider fixture, for development without cloud access
tagherd scan --fixture testdata/fixtures/scan.yaml
tagherd inventory --fixture testdata/fixtures/scan.yaml --cloud azure
tagherd plan --config testdata/fixtures/plan/tagherd.yaml \
  --fixture testdata/fixtures/scan.yaml --out /tmp/plan.json
tagherd apply --config testdata/fixtures/plan/tagherd.yaml \
  --fixture testdata/fixtures/scan.yaml --plan /tmp/plan.json --yes --verify \
  --terraform-fixture testdata/fixtures/terraform --pr
```

`scan` exits 0 when there are no findings in the `--fail-on` classes, 1 when
there are, 2 on usage, config or policy errors, and 3 when a scope (or part
of one, such as one region) could not be read. `inventory` and `plan` exit 0,
2 or 3; `plan` still writes the plan for the scopes it could read. `apply`
exits 0, 2 (including a plan made with another policy, a plan over 24 hours
old without `--allow-stale`, or a cancelled confirmation) or 4 when a change
failed or was stale. Unroutable Terraform changes do not change the exit
code.
Golden output for every format lives in `internal/cli/testdata/golden`;
refresh it with
`go test ./internal/cli -update` after checking a change.

## Plan values

For each fixable finding, `plan` takes the first source with a value that
passes the tag dictionary and the cloud's limits:

1. `--set Tag=Value`, for every resource in the run (missing, empty and
   invalid values)
2. `defaults.yaml` by type, group, scope, then cloud (missing and empty)
3. Parent tags: AWS account, Azure resource group, GCP project labels; never
   DeploymentType, never AdminEmail from a label
4. Built-in rules: DeploymentType defaults to Manual, case fixes,
   `ownership.yaml` for OwningOrg, `synonyms.yaml` for invalid values

Anything else is listed as unresolved with a reason. Manual resources route
`direct`, Automation `terraform`; a resource with an invalid DeploymentType
gets no changes. Catalog `partial` types, and GCP types outside the Compute,
Database and Storage groups, route `unwritable`. `tagherd init` writes
commented examples of the three value files.

## Apply

`apply` re-reads each resource's tags just before writing. A tag that
changed since the plan is skipped as `stale`, one already holding the new
value is left alone, and tags are never removed, so a second apply changes
nothing. A resource whose tags would break the cloud's limits (AWS 50 tags
and the `aws:` prefix, Azure 50, GCP 64 labels and label syntax) is not
written at all. GCP labels are written lowercase; AdminEmail goes to a
Resource Manager tag, whose value is created under the existing key when
missing. `--verify` reads the changed resources back through the writer,
not AWS Config, which can lag a write by minutes.

## Terraform route

Automation resources are changed in code, never through the cloud API, and
tagherd never runs `terraform plan` or `apply`. For each repository named by
a resource's `SCM`, `Org` and `Repo` tags, `apply`:

1. Checks it against `terraform.allowed_repositories` (globs, ignoring case)
2. Makes a shallow clone of the default branch, with the token from
   `terraform.scm_tokens` sent as an HTTP header, never in the URL
3. Finds the roots (directories with a `backend` or `cloud` block), runs
   `terraform init` and `terraform show -json` in each until the resource's
   ARN or ID turns up. Backend credentials come from the environment;
   `module_registries` tokens are passed as `TF_TOKEN_<host>`
4. Edits the tags where the code sets them: a literal `tags = {...}` map,
   the last object in `merge(local.common_tags, {...})` (or a new last
   argument), or, for a resource in a module, the root's module call. GCP
   edits `labels`, lowercase
5. Writes `<patch-dir>/<scm>-<org>-<repo>.patch`; with `--pr` it pushes
   `<branch_prefix><plan-id>` and opens a pull request listing every change

Only the edited lines change, and a file already in `terraform fmt` style is
kept that way. A change that cannot be placed is `unroutable`, with a
reason: `missing_repo_tags`, `not_allowlisted`, `not_in_state`,
`not_in_code`, or `unsupported_expression` (tags from a variable, a data
source or a non-literal value; a module call with no tags argument; GCP
Resource Manager tags, which live in `google_tags_tag_binding` resources).
A tag the code already sets is `noop`, so once the pull request is merged
the same plan proposes nothing. `--pr` asks for confirmation like a direct
write; patch files alone do not.

## Wiz override

The machine-image group's `override.AWS.missing` block is a Wiz graph query.
`scan` and `plan` send it to Wiz unchanged, once per run whatever the number
of accounts, and only the AMIs it returns can be reported as missing tags.
Invalid values, case mismatches and duplicate keys still come from each
AMI's own tags. Results are matched to the discovered AMIs by ARN or AMI
ID.

```yaml
wiz:
  api_url: https://api.<region>.app.wiz.io/graphql
  client_id: "${WIZ_CLIENT_ID}"
  client_secret: "${WIZ_CLIENT_SECRET}"
  # auth_url: https://auth.app.wiz.io/oauth/token   # the default
  # timeout: 60s                                    # the whole query, every page
```

Without the block, or when the secrets are unset, the call fails, it times
out, or Wiz caps the result set, the group falls back to the default tag
test. tagherd then logs one warning and the summary says
`Override fallback`. The fallback reports more AMIs, never fewer.

## Cloud access

Each cloud uses its own credential chain; tagherd stores no secrets.

| Cloud | Credentials | Read access |
| --- | --- | --- |
| AWS | Default chain or `--profile`, then `read_role_name` (and `write_role_name` for apply) assumed in each account | ReadOnlyAccess plus Config query access; Organizations read in the management or delegated admin account. Apply: `tag:TagResources`, `tag:GetResources` and the service tag permissions, IAM `Tag*`, and `organizations:TagResource` in the management account |
| Azure | `az login`, a service principal or managed identity | Reader on the subscriptions. Apply: Tag Contributor |
| GCP | Application Default Credentials, or `credentials_file` | Cloud Asset Viewer on the organization, folders or projects. Apply: each service's update permission for labels, Tag User, and Tag Admin to create tag values |

AWS reads AWS Config in every account and region unless
`scopes.aws.config_aggregator` names an aggregator, which turns that into one
query per account. Types Config does not record use per-service list calls
(`discovery: list:<adapter>` in the catalog).

Global flags work on every command. Each also reads `TAGHERD_<FLAG>` (for
example `TAGHERD_POLICY_DIR`), with precedence flag, then environment, then
`tagherd.yaml`, then default. Output is `table` on a terminal and `json`
otherwise; `-o ndjson|csv|markdown` are also available.

## Layout

```text
cmd/tagherd/          entry point
internal/cli/         cobra commands
internal/config/      tagherd.yaml loading and ${VAR} expansion
internal/policy/      tags.json and group files: load, validate, select
internal/catalog/     display name -> provider type catalog (catalog.yaml)
internal/wiz/         Wiz override query validation and the GraphQL client
internal/engine/      classify, match, evaluate and plan (pure)
internal/values/      defaults, ownership and synonyms files; --set
internal/scan/        discovery across scopes, then the engine; parent tags for plan
internal/apply/       plan checks, preconditions, limits, audit log, verify
internal/terraform/   Terraform route: allowlist, clone, state lookup, HCL edits, patches, PRs
internal/provider/    provider interfaces; fake/ for tests and fixtures
  aws/ azure/ gcp/    discovery adapters and tag writers, each behind a stub-able backend
internal/output/      renderers and exit codes
internal/model/       shared types
internal/ulid/        plan IDs
policy/               the tagging policy
```
