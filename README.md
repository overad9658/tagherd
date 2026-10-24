# tagherd

tagherd scans AWS, Azure and GCP resources against a tagging policy, writes a
reviewable plan of tag changes, and applies it. `scan` reads, `plan` decides,
`apply` writes: nothing changes in a cloud account unless `apply` runs.

- Spec: [spec/tagherd CLI Spec.md](spec/tagherd%20CLI%20Spec.md)
- Component designs and backlog: linked at the end of the spec

## Status

Phase 1 (offline foundation) is implemented: config, policy loading and
validation, the type catalog, and the `init`, `policy`, `catalog`, `version`
and `completion` commands. `scan`, `inventory`, `plan` and `apply` are
registered but report the phase they arrive in.

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
tagherd init                            # tagherd.yaml, value files, policy scaffold
```

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
internal/output/      renderers and exit codes
internal/model/       shared types
policy/               the tagging policy
```
