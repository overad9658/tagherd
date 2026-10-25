# tagherd

tagherd scans AWS, Azure and GCP resources against a tagging policy, writes a
reviewable plan of tag changes, and applies it. `scan` reads, `plan` decides,
`apply` writes: nothing changes in a cloud account unless `apply` runs.

- Spec: [spec/tagherd CLI Spec.md](spec/tagherd%20CLI%20Spec.md)
- Component designs and backlog: linked at the end of the spec

## Status

Phases 1 to 5 are implemented:

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
  `--verify`. Terraform changes are listed as not applied until phase 6

The adapters are unit-tested against stubs; they have not yet run against
real accounts (the pilot in the backlog).

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

# Write the plan's direct changes; every attempt goes to tagherd-audit.ndjson
tagherd apply --plan plan.json            # asks for confirmation
tagherd apply --plan plan.json --yes --verify -o markdown

# The fake-provider fixture, for development without cloud access
tagherd scan --fixture testdata/fixtures/scan.yaml
tagherd inventory --fixture testdata/fixtures/scan.yaml --cloud azure
tagherd plan --config testdata/fixtures/plan/tagherd.yaml \
  --fixture testdata/fixtures/scan.yaml --out /tmp/plan.json
tagherd apply --config testdata/fixtures/plan/tagherd.yaml \
  --fixture testdata/fixtures/scan.yaml --plan /tmp/plan.json --yes --verify
```

`scan` exits 0 when there are no findings in the `--fail-on` classes, 1 when
there are, 2 on usage, config or policy errors, and 3 when a scope (or part
of one, such as one region) could not be read. `inventory` and `plan` exit 0,
2 or 3; `plan` still writes the plan for the scopes it could read. `apply`
exits 0, 2 (including a plan made with another policy, a plan over 24 hours
old without `--allow-stale`, or a cancelled confirmation) or 4 when a change
failed or was stale.
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
internal/wiz/         Wiz override query validation
internal/engine/      classify, match, evaluate and plan (pure)
internal/values/      defaults, ownership and synonyms files; --set
internal/scan/        discovery across scopes, then the engine; parent tags for plan
internal/apply/       plan checks, preconditions, limits, audit log, verify
internal/provider/    provider interfaces; fake/ for tests and fixtures
  aws/ azure/ gcp/    discovery adapters and tag writers, each behind a stub-able backend
internal/output/      renderers and exit codes
internal/model/       shared types
internal/ulid/        plan IDs
policy/               the tagging policy
```
