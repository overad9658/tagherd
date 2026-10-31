# tagherd CLI Spec

Oct 22, 2026 · @Jeff

## Overview

tagherd is a command-line tool that scans AWS, Azure and GCP resources against a tagging policy and applies the missing or incorrect tags. The policy is the set of 17 resource-group files plus the tag dictionary (`tags.json`) you provided; tagherd reads them as they are.

**Goals**

- Scan any mix of AWS accounts, Azure subscriptions and GCP projects and report every resource that breaks policy.
- Produce a reviewable plan of tag changes, then apply it.
- Treat the policy files as the single source of truth for which resource types need which tags.
- Run the same way on a laptop and in CI, with stable exit codes and machine-readable output.

**Non-goals**

- It does not create or attach cloud-native policies (AWS SCP or tag policies, Azure Policy, GCP organization policies). It only validates and fixes tags itself.
- It does not integrate with a CMDB. That is future work.
- The API and UI are separate specs; the CLI is designed so they can call the same engine.
- It does not delete tags it does not own. Tags outside the dictionary are left alone.

**Inputs**

| Input | Purpose |
| --- | --- |
| `tags.json` | Tag dictionary: each key maps to its allowed values, or `null` when any value is allowed |
| 17 group files | Each lists required tags, target resource types per cloud, optional filters and optional overrides |
| `tagherd.yaml` | Scopes, credentials, repository allowlist (see Configuration) |
| `ownership.yaml`, `defaults.yaml`, `synonyms.yaml` | Owning org by scope, default tag values by scope, and corrections for invalid values (see Apply behavior) |

## Policy model

A resource is evaluated against exactly the tags its group requires, and a tag value is valid only if the dictionary allows it.

**Tag dictionary (`tags.json`)**

| Tag | Allowed values | Check |
| --- | --- | --- |
| Environment | Production, Staging, Development, Testing, CI, DTS | Enumerated |
| DeploymentType | Manual, Automation | Enumerated; drives how fixes are applied |
| Brand | any | Present and non-empty |
| AppCategory | any | Present and non-empty |
| Exposure | Internal, External | Enumerated |
| AdminEmail | any | Present, non-empty, valid email format (proposed) |
| OwningOrg | ITOps, DevOps, InfoSec, Finance, Platform | Enumerated |
| EndpointType | Server, Workstation | Enumerated |
| DataClassification | Public, Internal, Confidential, Sensitive | Enumerated |

Key and value matching ignores case. `production` matches `Production`, and `environment` matches `Environment`. The dictionary spelling is the canonical form that tagherd writes. For free-form tags (`null` in the dictionary) the existing spelling is preserved.

**Group files**

| Field | Meaning |
| --- | --- |
| `name` | Group name shown in reports |
| `tags` | Required tag keys for every resource in the group |
| `target.AWS / Azure / GCP` | Resource type display names in scope for that cloud; an empty list means nothing is checked there |
| `filters.<cloud>.name.DOES_NOT_CONTAIN` | Resources whose name contains any listed string are excluded (substring, case-insensitive) |
| `override.<cloud>.missing` | Replaces the default "is this resource missing tags" test for that cloud |
| `override.<cloud>.all` | `null` in the current files: no replacement for the default scope |

Three groups use `filters` (Management & Governance, Compute w/o machines and images, Integration & Messaging), all for AWS, to skip AWS Control Tower notification resources. Only one group, Compute (only machine images), uses `override`.

**Group file schema.** The files in `./policy` use exactly these top-level keys. `policy validate` rejects unknown keys.

```jsonc
{
  "name": "Database",                 // required, unique across groups (case-insensitive)
  "description": null,                // string or null
  "tags": ["Environment", "..."],     // required, non-empty; each key must exist in tags.json (case-insensitive)
  "target": {                         // required; all three keys present, each a list (may be empty)
    "AWS": ["AWS DynamoDB Table"], "Azure": [], "GCP": []
  },
  "filters": {                        // optional; per cloud; only name.DOES_NOT_CONTAIN in v1
    "AWS": { "name": { "DOES_NOT_CONTAIN": ["aws-controltower-..."] } }
  },
  "override": {                       // optional; per cloud
    "AWS": { "all": null, "missing": { /* graph query, see below */ } }
  }
}
```

Each display name under `target.<cloud>` must start with that cloud's prefix (`AWS `, `Azure `, `GCP `). A name with the wrong prefix is a validation error. Groups are selected with `--group` by `name` (case-insensitive) or by file stem, such as `database_only_snapshots`.

**Resolution rules**

1. A resource enters a group when its cloud and type match a `target` name and it passes the group's filters.
2. Required tags are the group's `tags` list. If a type appears in more than one group, the required set is the union and `policy validate` warns.
3. For each required tag, tagherd records one result: compliant, missing, empty, invalid value, or case mismatch (see Scan behavior).
4. If an `override` exists for that cloud, its `missing` test decides which resources are reported as missing tags instead of rule 3.

**The machine-image override**

As written, an AWS AMI is reported when all of the following hold: it has `nativeType` `ami`; at least one of DeploymentType (Manual or Automation), OwningOrg (one of the five orgs) or AdminEmail is absent; and it has no `USES` relationship to an `ec2#amitags` service configuration that carries a valid value for any of those three. tagherd v1 supports the subset of the query syntax used in this file (`EQUALS`, `TAG_CONTAINS_ANY`, `TAG_DOES_NOT_CONTAIN_ANY`, `_or`, and a negated `relationships` clause) and rejects any other operator at `policy validate` time. This syntax and the `ec2#amitags` object are read from the existing inventory graph. If tagherd cannot read that data, the AMI group falls back to the default tag test.

The query also uses a top-level `type` list (`SERVICE_CONFIGURATION`, `VIRTUAL_MACHINE_IMAGE`), `select: true`, and a `cloudPlatform` field, so v1 must accept those too. The vocabulary (entity types, `nativeType`, `USES` relationships, `select`) matches the Wiz security graph query format. The inventory graph is Wiz (confirmed). tagherd sends the query to the Wiz GraphQL API unchanged, using a service account configured in a `wiz` block in `tagherd.yaml`. Semantics to note:

- `TAG_DOES_NOT_CONTAIN_ANY` with only a `key` means the key is absent. With `key` and `value` it means no listed key/value pair is present.
- The query checks AdminEmail for presence only, not email format. The override decides only which AMIs count as missing tags; `INVALID_VALUE` and `CASE_MISMATCH` findings still come from the default checks.
- An AMI is exempt when its `ec2#amitags` configuration carries a valid value for **any one** of the three tags, even if the other two are missing.
- The override repeats the DeploymentType and OwningOrg values instead of referencing `tags.json`. `policy validate` warns when those values drift from the dictionary.

## Resource coverage

The 17 groups name 265 resource types: 147 for AWS, 80 for Azure and 38 for GCP. DeploymentType is required in every group, and AdminEmail and OwningOrg in every group except the subnet, security group and route table group.

| Group | Required tags | AWS types | Azure types | GCP types |
| --- | --- | --- | --- | --- |
| Analytics | Environment, DeploymentType, Brand, AppCategory, Exposure, AdminEmail, OwningOrg, DataClassification | 6 | 1 | 0 |
| Compute (only machines) | Environment, DeploymentType, Brand, AppCategory, Exposure, AdminEmail, OwningOrg, EndpointType | 1 | 1 | 1 |
| Compute (only machine images) | DeploymentType, AdminEmail, OwningOrg | 1 | 1 | 1 |
| Compute (w/o machines and images) | Environment, DeploymentType, Brand, AppCategory, Exposure, AdminEmail, OwningOrg | 10 | 6 | 2 |
| Containers & Kubernetes | Environment, DeploymentType, Brand, AppCategory, Exposure, AdminEmail, OwningOrg | 6 | 5 | 2 |
| Containers & Kubernetes (registries) | DeploymentType, Brand, Exposure, AdminEmail, OwningOrg | 2 | 1 | 1 |
| Database | Environment, DeploymentType, Brand, AppCategory, Exposure, AdminEmail, OwningOrg, DataClassification | 25 | 15 | 6 |
| Database (only snapshots) | Environment, DeploymentType, Brand, AppCategory, AdminEmail, OwningOrg, DataClassification | 2 | 0 | 0 |
| Developer Tools | DeploymentType, Brand, AppCategory, AdminEmail, OwningOrg | 4 | 0 | 0 |
| Integration & Messaging | Environment, DeploymentType, Brand, AppCategory, AdminEmail, OwningOrg, DataClassification | 6 | 5 | 2 |
| Machine Learning | Environment, DeploymentType, Brand, AppCategory, Exposure, AdminEmail, OwningOrg, DataClassification | 6 | 3 | 10 |
| Management & Governance | Environment, DeploymentType, Brand, AppCategory, AdminEmail, OwningOrg | 14 | 9 | 1 |
| Networking (w/o subnet, SG, route tables) | Environment, DeploymentType, Brand, AppCategory, AdminEmail, OwningOrg | 28 | 16 | 3 |
| Networking (only subnet, SG, route tables) | Environment, DeploymentType, Brand, AppCategory | 3 | 3 | 0 |
| Security, Identity & Compliance | Environment, DeploymentType, Brand, AppCategory, AdminEmail, OwningOrg | 20 | 8 | 5 |
| Storage | Environment, DeploymentType, Brand, AppCategory, Exposure, AdminEmail, OwningOrg, DataClassification | 8 | 5 | 1 |
| Storage (w/o network access) | Environment, DeploymentType, Brand, AppCategory, AdminEmail, OwningOrg, DataClassification | 5 | 1 | 3 |

The AWS count for Storage includes one entry, "Azure Storage Account", that looks misplaced (see Open questions).

**Type catalog.** The policy names types in display form ("AWS RDS Aurora MySQL Instance"). tagherd ships a catalog that maps each display name to a provider resource type, such as an AWS Config type, an Azure resource type or a GCP asset type, plus whether the type can hold tags. `tagherd catalog` lists it, and `policy validate` fails on any display name the catalog does not know.

Many display names share one provider type, so each catalog entry also needs a discriminator, a predicate on resource attributes:

| Display names | Shared provider type | Discriminator |
| --- | --- | --- |
| RDS MySQL / PostgreSQL / MariaDB / Oracle / MSSQL / Aurora Instance | `AWS::RDS::DBInstance` | `engine`, plus whether the instance belongs to a cluster |
| ELB V2 Application / Network Load Balancer | `AWS::ElasticLoadBalancingV2::LoadBalancer` | `type` |
| EBS Encrypted / Unencrypted Snapshot | `AWS::EC2::Snapshot` | `encrypted` |
| IAM AWS Managed / Customer Managed Policy | `AWS::IAM::Policy` | ARN account (`aws` vs account ID) |
| Azure Function / Azure App Service | `Microsoft.Web/sites` | `kind` contains `functionapp` |
| Azure Database for MySQL / PostgreSQL (Server vs Flexible) | `Microsoft.DBforMySQL/servers` vs `.../flexibleServers` | type only |

The catalog entry schema is: `display`, `cloud`, `native_type`, optional `match` (attribute → value or list), `taggable` (`true`, `false` or `partial`), `write_api`, and optional `write_scope` (for example, `org-management-account`). If more than one entry matches a resource, catalog validation fails.

## CLI command surface

tagherd has three working commands: `scan` reads, `plan` decides, `apply` writes. Nothing changes in a cloud account unless `apply` runs.

| Command | What it does | Writes to cloud |
| --- | --- | --- |
| `tagherd init` | Creates `tagherd.yaml` and a policy directory scaffold | No |
| `tagherd policy validate` | Checks schema, tag dictionary references, catalog names, overlaps and override syntax | No |
| `tagherd policy list` / `show <group>` | Lists groups, or prints one group with its resolved tags and types | No |
| `tagherd catalog` | Lists known resource types and whether each is taggable | No |
| `tagherd scan` | Discovers resources, evaluates them, reports findings | No |
| `tagherd plan` | Runs a scan, then writes a plan file of proposed tag changes | No |
| `tagherd apply --plan plan.json` | Executes a plan: direct tag writes, or Terraform changes | Yes |
| `tagherd inventory` | Lists discovered resources with their current tags | No |
| `tagherd version`, `completion` | Version info and shell completion | No |

**Global flags**

| Flag | Meaning |
| --- | --- |
| `--config <file>` | Config file (default `./tagherd.yaml`, then `~/.config/tagherd/`) |
| `--policy-dir <dir>` | Directory with group files and `tags.json` |
| `--cloud aws,azure,gcp` | Limit to clouds |
| `--account`, `--subscription`, `--project` | Limit to scopes; repeatable; globs allowed |
| `--group <name>` | Limit to policy groups |
| `--type <display name>` | Limit to resource types |
| `--output table\|json\|ndjson\|csv\|markdown` | Output format (default `table` on a terminal, `json` otherwise) |
| `--out <file>` | Write output to a file |
| `--concurrency <n>` | Parallel scopes (default 8) |
| `--profile <name>` | AWS shared-config profile used as the base credential before assuming per-account roles |
| `-v`, `-q`, `--no-color` | Verbosity and color |

**Command-specific flags**

| Command | Flag | Meaning |
| --- | --- | --- |
| `scan` | `--fail-on any\|missing\|invalid\|none` | Which finding classes make the exit code 1 (default `any`; see Finding types) |
| `scan` | `--summary` | Show compliance percentages only, by group, cloud and scope |
| `plan` | `--set Tag=Value` | Supply a value for a tag that cannot be inferred; repeatable |
| `plan` | `--defaults <file>` | Scope-level default values (see Apply behavior) |
| `plan` | `--grace <duration>` | Skip resources created more recently than this (default 5m) |
| `apply` | `--plan <file>` | Required. The plan to execute |
| `apply` | `--yes` | Skip the interactive confirmation (required in CI) |
| `apply` | `--only direct\|terraform` | Execute one write path only |
| `apply` | `--pr` | Open pull requests for Terraform changes instead of only writing patch files |
| `apply` | `--verify` | Re-scan changed resources afterward and report any still non-compliant |
| `apply` | `--allow-stale` | Accept a plan more than 24 hours old (never overrides a policy hash mismatch) |
| `plan` | `--out <file>` | The plan file path (the global flag; defaults to `plan.json` for `plan`) |

**Examples**

```bash
# Report non-compliant databases in two AWS accounts
tagherd scan --cloud aws --account 1111* --group Database

# Build a plan for everything, supplying one value that cannot be inferred
tagherd plan --defaults defaults.yaml --out plan.json

# Review, then apply and open pull requests for Automation resources
tagherd apply --plan plan.json --pr --verify

# CI gate: fail the build on any finding
tagherd scan --output json --fail-on any
```

## Scan behavior

A scan is read-only and deterministic: the same policy and the same cloud state always produce the same findings.

1. **Load and validate policy.** Parse `tags.json` and all group files. Stop with exit code 2 on any error.
2. **Resolve scopes.** Expand the configured AWS accounts, Azure subscriptions and GCP projects, then apply `--account`, `--subscription` and `--project` filters.
3. **Discover.** Use each cloud's bulk inventory source first (see Provider specifics), restricted to the resource types the selected groups name. Fall back to per-service list calls for types the inventory does not cover.
4. **Normalize.** Turn every resource into one record: cloud, scope, region, display type, native ID, name, created time (if known), and tags with keys and values as found.
5. **Match.** Assign resources to groups by type and filters. Resources of types no group names are ignored and counted as out of scope.
6. **Evaluate.** Check each required tag and emit findings.
7. **Report.** Print findings and a summary; exit with a code based on `--fail-on`.

**Finding types**

| Code | Meaning | Auto-fixable |
| --- | --- | --- |
| `MISSING_TAG` | Required key is absent | Only when a value source exists (see Apply behavior) |
| `EMPTY_VALUE` | Key exists with an empty or whitespace value | Same as above |
| `INVALID_VALUE` | Value is not in the dictionary's allowed list, or AdminEmail is not a valid email | Yes, when `synonyms.yaml` has an entry for the value; otherwise reported for a human |
| `CASE_MISMATCH` | Key or value matches the dictionary except for case, such as `production` | Yes; rewritten to the canonical spelling |
| `DUPLICATE_KEY` | Two keys differ only by case and hold conflicting values, such as `environment` and `Environment` | No |
| `INVALID_DEPLOYMENT_TYPE` | DeploymentType is present but is neither Manual nor Automation | No; marked invalid, never overwritten |
| `NOT_TAGGABLE` | The type cannot hold tags in this cloud | No; excluded from compliance counts |
| `UNMAPPED_TYPE` | A policy type has no discovery adapter yet | No; listed as a coverage gap |

A missing DeploymentType is treated as Manual, so the finding is `MISSING_TAG` with a proposed fix of `DeploymentType=Manual`.

**`--fail-on` classes.** The classes are sets, not a severity ladder. `missing` covers `MISSING_TAG` and `EMPTY_VALUE`. `invalid` covers `INVALID_VALUE`, `CASE_MISMATCH`, `DUPLICATE_KEY` and `INVALID_DEPLOYMENT_TYPE`. `any` is both classes, and `none` never returns 1. `NOT_TAGGABLE` and `UNMAPPED_TYPE` never affect the exit code, and unreadable scopes are reported through exit code 3 instead.

**Case rules per cloud.** `CASE_MISMATCH` on a key and `DUPLICATE_KEY` cannot happen on Azure, because tag names there ignore case. On GCP labels, lowercase keys and values are compliant, so the check applies only to Resource Manager tags.

**Compliance numbers.** A resource is compliant when it has no findings other than `NOT_TAGGABLE`. The summary reports compliant over evaluated resources by group, cloud and scope, plus a count of findings per tag. Resources that tagherd could not read (permission errors, unreachable scopes) are listed separately and never counted as compliant.

**Performance target.** A full scan of more than 300 scopes and more than 15,000 resources should finish in under 15 minutes on one machine with the default concurrency, using bulk inventory queries rather than one call per resource. This is a design target to confirm in a pilot, not a measured result.

**Example finding (JSON)**

```json
{
  "cloud": "AWS",
  "scope": "111122223333",
  "region": "us-east-1",
  "group": "Database",
  "type": "AWS RDS PostgreSQL Instance",
  "id": "arn:aws:rds:us-east-1:111122223333:db:orders",
  "name": "orders",
  "finding": "CASE_MISMATCH",
  "tag": "Environment",
  "current": "production",
  "proposed": "Production",
  "fixable": true
}
```

## Apply behavior

tagherd only writes a tag when it has a valid value from a known source, and it writes in the way the resource is managed: directly for manual resources, through Terraform for automated ones.

**Plan file.** `plan` writes JSON with a plan ID, creation time, a hash of the policy, the scopes covered, and a list of changes. Each change holds the resource, tag, action (`ADD` or `UPDATE`), current value, new value, the rule that produced the value, the write route, and the value seen at plan time (used as a precondition).

**Where values come from.** For each fixable finding, tagherd takes the first source that yields a value:

| Order | Source | Covers |
| --- | --- | --- |
| 1 | `--set Tag=Value` | Any tag, for every resource in the filtered run |
| 2 | `defaults.yaml` | Any tag, by cloud, scope, group or type (for example, Brand and AppCategory per account) |
| 3 | Parent tags | AWS account tags, Azure resource group tags and GCP project labels; the inherited value must pass the dictionary; never used for DeploymentType |
| 4 | Built-in rules | DeploymentType defaults to Manual; case mismatches are rewritten to the dictionary spelling; OwningOrg comes from the ownership map; invalid values are corrected through the synonym map |
| 5 | None | Finding stays open and is listed as unresolved |

tagherd never guesses Brand, AppCategory, AdminEmail, Environment, Exposure, DataClassification or EndpointType. They come only from `--set`, `defaults.yaml` or a parent's tags; otherwise they stay unresolved. AdminEmail is not inherited from GCP project labels, because a label cannot hold an email address. The ownership map is a local file in v1 that maps accounts, subscriptions and projects to one of ITOps, DevOps, InfoSec, Finance or Platform; importing it from the shared spreadsheet is a later feature.

**Synonym map.** An `INVALID_VALUE` is corrected when `synonyms.yaml` maps the value found to a dictionary value. Matching ignores case, the target must be in the dictionary, and a value with no entry stays unresolved. The map never applies to DeploymentType, whose invalid values are always left alone.

```yaml
Environment:
  prod: Production
  prd: Production
  dev: Development
OwningOrg:
  it-ops: ITOps
  security: InfoSec
```

**Write route by DeploymentType**

| DeploymentType | Route | What happens |
| --- | --- | --- |
| Manual, or missing | Direct | tagherd writes the tags through the cloud API. A missing DeploymentType is first set to Manual |
| Automation | Terraform | tagherd produces a Terraform change instead of touching the resource, because the next Terraform run would revert a direct write |
| Anything else | None | Marked `INVALID_DEPLOYMENT_TYPE`; nothing is written |

**Terraform route.** The resource's repository is identified by three tags: `SCM` (GitHub or BitBucket), `Org` and `Repo`. tagherd checks the repository against the allowlist in `tagherd.yaml` (editable, and extendable by adding entries), finds the resource's Terraform address from the root's state, and edits the tags where the code sets them: a literal `tags` map, or a `merge(local.common_tags, {...})` expression. For resources inside shared modules, the edit goes at the module call that passes the tags. The result is a patch file; with `--pr`, tagherd also commits to a branch and opens a pull request against the repository's default branch (GitHub, or Bitbucket Cloud). It never runs `terraform apply`. If the repository tags are missing, the repository is not on the allowlist, or the resource cannot be found in code, the change is reported as unroutable and nothing is written.

**Safety rules**

- Additive only: tagherd adds or updates the tags in the plan and never removes any tag.
- Valid values only: a value that fails the dictionary is never written.
- Preconditions: before each write, tagherd re-reads the tag; if it no longer matches the plan, the change is skipped as stale.
- Idempotent: applying the same plan twice changes nothing the second time.
- Grace period: resources created less than `--grace` ago (default 5 minutes) are skipped so pipelines can tag them first. Resources with no known creation time are not skipped.
- Staleness: `apply` refuses a plan whose policy hash differs from the current policy, and asks for `--allow-stale` when the plan is more than 24 hours old.
- Confirmation: `apply` prints a summary by route and cloud and waits for confirmation unless `--yes` is set.
- Audit log: every attempted write is appended to `tagherd-audit.ndjson` with the actor, resource, tag, old value and new value.
- Partial failure: one failed change does not stop the others; the run ends with a summary and exit code 4.

## Provider specifics

Each cloud has a discovery adapter and a tag-writer adapter behind one common interface, so the policy engine never sees cloud-specific details.

|  | AWS | Azure | GCP |
| --- | --- | --- | --- |
| Credentials | Standard AWS credential chain, `--profile`, and an assumable role name per account | Azure default credential chain: `az login`, service principal, or managed identity | Application Default Credentials or a service account key |
| Scope list | AWS Organizations accounts, filtered by include and exclude lists | Subscriptions in the tenant | Projects under the configured organization or folders |
| Bulk discovery | AWS Config (enabled in every account), via an aggregator where one exists | Azure Resource Graph | Cloud Asset Inventory |
| Fallback discovery | Per-service list calls | ARM list calls | Per-service list calls |
| Tag write | Resource Groups Tagging API where the type supports it, otherwise the service's own tagging call | Tags API at resource scope with the merge operation | Labels through each service's update call; Resource Manager tags through tag bindings |
| Case rules | Keys and values are case-sensitive in the cloud; tagherd matches ignoring case and reports `CASE_MISMATCH` | Tag names ignore case, values do not | Label keys and values must be lowercase |
| Limits to respect | 50 user tags per resource; keys to 128 characters, values to 256; `aws:` prefix reserved and never touched | 50 tag name and value pairs per resource | 64 labels per resource; keys and values to 63 characters |
| Typical read role | ReadOnlyAccess plus Config query access | Reader | Cloud Asset Viewer |
| Typical write role | Tag write permissions for the in-scope types | Tag Contributor | Per-service update permissions, plus tag admin and tag user roles for Resource Manager tags |

**GCP labels and Resource Manager tags**

tagherd supports both mechanisms on GCP, because different resource types accept different ones.

- Labels are checked and written under lowercase keys, so `DeploymentType` is stored as `deploymenttype` and `Production` as `production`. Because matching ignores case, this counts as compliant.
- Resource Manager tags use tag keys and tag values defined in the organization or project, bound to resources. When a dictionary value has no tag value yet, `apply` creates it; when a binding holds the wrong value, `apply` replaces it.
- Label values allow only lowercase letters, digits, underscores and dashes, so AdminEmail is never written as a label. On GCP it is checked and written only as a Resource Manager tag.
- Initial write scope on GCP: Compute, Database and Storage groups. Other GCP types, including networking, are scanned and reported but not written until a later release.

**Taggability.** Each adapter declares per type whether tags can be written. Directory objects such as Microsoft Entra ID roles, users and groups, and customer-unwritable objects such as AWS managed policies, are expected to be `NOT_TAGGABLE`. The exact list comes from the type catalog and is confirmed during the pilot.

## Configuration, output and exit codes

Configuration lives in one YAML file. Secrets never go in it: any value can reference an environment variable as `${NAME}`, and cloud credentials come from each cloud's own credential chain.

```yaml
policy_dir: ./policy            # tags.json and the 17 group files
ownership_file: ./ownership.yaml # scope -> OwningOrg
defaults_file: ./defaults.yaml   # optional scope-level tag defaults
synonyms_file: ./synonyms.yaml   # invalid value -> dictionary value

scopes:
  aws:
    organization: true
    read_role_name: tagherd-read
    write_role_name: TAGHERD-write
    exclude_accounts: ["000000000000"]
  azure:
    tenant_id: "${AZURE_TENANT_ID}"
    subscriptions: ["*"]
  gcp:
    organization_id: "${GCP_ORG_ID}"
    projects: ["*"]

terraform:
  allowed_repositories:         # entries can be added over time
    - { scm: GitHub, org: example-org, repo: "*" }
    - { scm: BitBucket, org: example-ws, repo: "infra-*" }
  pull_requests:
    branch_prefix: tagherd/
  module_registries:
    - { type: jfrog, url: "${JFROG_URL}", token: "${JFROG_TOKEN}" }
  scm_tokens:
    github: "${GITHUB_TOKEN}"
    bitbucket: "${BITBUCKET_TOKEN}"
```

Precedence is command-line flag, then environment variable (`TAGHERD_<FLAG>`, upper case with dashes as underscores, such as `TAGHERD_POLICY_DIR`), then config file, then default.

**Output**

| Format | Use |
| --- | --- |
| `table` | Human reading in a terminal |
| `json` | One document: summary plus all findings or changes |
| `ndjson` | One finding or change per line, for streaming into other tools |
| `csv` | Spreadsheets |
| `markdown` | Summary for pull request comments and CI logs |

Results go to standard output and logs to standard error, so output can be piped. Color is off when output is not a terminal.

**Exit codes**

| Code | Meaning |
| --- | --- |
| 0 | Success: no findings in the `--fail-on` classes, or the apply finished cleanly |
| 1 | At least one finding in the `--fail-on` classes |
| 2 | Usage, configuration or policy error; nothing was scanned or written |
| 3 | Scan incomplete: at least one scope could not be read; results for the rest are still printed |
| 4 | Apply finished with at least one failed or stale change |

When more than one code applies, the highest is returned.

## Issues in the policy files and decisions

The policy files have the items below worth fixing or confirming. Only the "Azure Storage Account" placement stops `policy validate` from passing. All ten decisions below are reflected in the sections above.

**Issues found in the policy files**

| File | Issue | Suggested action |
| --- | --- | --- |
| `storage.json` | "Azure Storage Account" is listed under the AWS targets, so Azure storage accounts are not checked | Move it to the Azure list |
| `database_only_snapshots.json` | Azure and GCP lists are empty, so database snapshots are covered on AWS only | Add Azure and GCP types, or confirm AWS-only is intended |
| `developer_tools.json` | Azure and GCP lists are empty | Same |
| `analytics.json` | GCP list is empty | Same |
| `networking_and_content_delivery_only_subnet_sg_route.json` | GCP list is empty, and the group does not require AdminEmail or OwningOrg | Confirm both are intended |
| `security_identity_and_compliance.json` | GCP "Secret" and "Secret Manager Secret", and "KMS Key" and "Cloud KMS Crypto Key", look like duplicates | Keep one of each, or document them as aliases |
| `machine_learning.json` | Several GCP Vertex AI workbench types overlap (managed notebook and managed workbench; user-managed notebook and user-managed workbench) | Keep distinct types only |
| `containers_and_kubernetes.json` | "AWS Elastic Container Service (ECS)" and "AWS ECS Service" are ambiguous; the first may mean the cluster | Rename to say which |
| `integration_and_messasing.json` | File name has a typo ("messasing") | Rename; no effect on behavior |
| All group files | `description` is `null` | Fill in; reports can show it |
| `compute_only_machine_images.json` | The override repeats the DeploymentType and OwningOrg values instead of referencing `tags.json`, and exempts an AMI when any one of the three tags is valid on its `ec2#amitags` config | Keep in sync with the dictionary (`policy validate` warns). The any-one exemption is intended |
| `management_and_governance.json` | "AWS Backup Plan Rule" means the backup plan. "AWS CloudWatch Metric Filter Alarm" means the alarm, which "AWS CloudWatch Alarm" in the same group already covers | Rename to "AWS Backup Plan"; remove the Metric Filter Alarm entry (until then the catalog treats it as an alias) |
| Group names | File names differ from the display names, such as "Containers & Kubernetes (Container Registries)" and "Security, Identity, & Compliance" | None; `--group` accepts either |

**Expected non-taggable or special-case types (confirm in pilot).** The type catalog will mark these, and they feed `NOT_TAGGABLE` and the write scope:

| Cloud | Types | Why |
| --- | --- | --- |
| AWS | IAM AWS Managed Policy, IAM Root User Policy | Customers cannot tag them |
| AWS | Redshift Datashare | No tag API on the datashare |
| AWS | Account, Organizational Unit, SCP, RCP | Taggable only from the Organizations management account, so the write role must exist there |
| Azure | Microsoft Entra ID Tenant, Role, User and Group; Management Group; Diagnostic Settings | Not ARM-taggable |
| Azure | Blob Storage Container, File Share, ACI Container, Scale Set Virtual Machine | Sub-resources with metadata or inherited tags, not ARM tags |
| Azure | Key Vault Certificate | Data-plane tags; needs a separate Key Vault writer and permission |
| Azure | Network Security Group (Classic) | Classic (ASM) resource; not taggable |
| Azure | Databricks Catalog, OpenAI Fine Tuning Job, OpenAI Model | Not ARM resources |
| GCP | Compute Instance Template; Vertex AI Training, Pipeline Run, Batch Prediction Job | Labels are fixed at creation; scan only, report as not writable |

**Notes from loading the files.** All 18 files parse. The counts match this spec: 147 AWS, 80 Azure and 38 GCP types, 265 in total. Every required tag exists in `tags.json`. No type appears in more than one group, so the union rule is not exercised yet. The only prefix error is the "Azure Storage Account" entry listed above.

**OCI targets.** Every group file has an `OCI` list: 85 Oracle Cloud types mapped from the AWS, Azure and GCP equivalents in the same group. OCI has no discovery adapter or tag writer yet, so the catalog marks every OCI type `discovery: none`. They show in `tagherd catalog --gaps`, and a scan reports them as `UNMAPPED_TYPE`. Native types are OCI Search Service resource types, to be confirmed when the adapter is written. Mapping choices worth confirming:

| Group | OCI choice | Why |
| --- | --- | --- |
| Management & Governance | Compartment stands in for AWS Account, Azure Subscription and Resource Group, and GCP Project | OCI has one hierarchy; the root compartment is the tenancy, and its updates go to the home region |
| Management & Governance | Resource Manager Stack, Monitoring Alarm, Logging Log Group, Events Rule, Block Volume Backup Policy | Equivalents of CloudFormation, CloudWatch alarms and log groups, EventBridge and AWS Backup plans |
| Containers & Kubernetes | Compute Instance Pool stands in for AWS Auto Scaling Group and Azure scale sets | Pool members are ordinary Compute Instances and are checked by "Compute (only machines)" |
| Compute (only machine images) | Compute Image, no override | OCI images carry their own tags; the AMI override exists because of `ec2#amitags` and has no OCI counterpart |
| Security, Identity, & Compliance | IAM User, Group and Dynamic Group are `partial` | In identity-domain tenancies they are domain (SCIM) objects, not IAM resources; scan only |
| Database (only snapshots), Developer Tools | MySQL HeatWave Backup; the four DevOps types | These groups now cover OCI even though Azure and GCP are still empty |
| Storage | Object Storage Bucket only | AWS Backup Vault and Azure Recovery Services Vault have no OCI equivalent |
| All groups | No OCI types for AWS Kinesis Video, Athena, SSM, Step Functions, WorkSpaces, Shield, SSH key pairs or Audit | No taggable OCI resource matches |

Filters have no OCI entries. The nearest equivalent of the Control Tower exclusions is Oracle's `ManagedCompartmentForPaaS` compartment, worth a `filters.OCI` entry once OCI is scanned. OCI has freeform and defined tags. Defined tags (namespace.key, with optional list-of-values validation) map best onto `tags.json`, but that is a decision for the writer.

**Decisions**

1. **Override syntax.** tagherd can read the inventory data the machine-image override needs. If it cannot, the AMI group falls back to the default tag test.
2. **Inheritance.** tagherd also inherits values from AWS account tags, Azure resource group tags and GCP project labels, so `apply` can fill tags beyond DeploymentType, case, OwningOrg and synonym fixes.
3. **AdminEmail on GCP.** Written only as a Resource Manager tag, never as a label.
4. **Non-taggable types.** Types a cloud cannot tag are excluded from compliance counts rather than counted as failures.
5. **Types in several groups.** The union of required tags is the rule.
6. **Invalid values.** `INVALID_VALUE` is auto-fixed through a synonym map (`synonyms.yaml`); DeploymentType is excluded.
7. **Inventory graph.** The inventory graph is Wiz.
8. **AMI exemption.** An AMI is exempt when its `ec2#amitags` config carries a valid value for any one of DeploymentType, OwningOrg or AdminEmail.
9. **Image overrides.** Azure and GCP machine-image types have no override, so they use the default test.
10. **Backup plan and alarm.** The policy means the AWS backup plan and the CloudWatch alarm, not the plan rule or the metric filter.

## Component designs and backlog

The spec is broken into per-component designs in [tagherd Component Designs](https://claude.ai/artifact/91U6EaTnd3t4ZJdX5B2nZz), with the phase-by-phase plan in [tagherd Backlog](https://claude.ai/artifact/Coz2d3ejVbUainJudbCuw6).
