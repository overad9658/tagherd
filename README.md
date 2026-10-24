# tagherd

tagherd scans AWS, Azure and GCP resources against a tagging policy, writes a
reviewable plan of tag changes, and applies it. `scan` reads, `plan` decides,
`apply` writes: nothing changes in a cloud account unless `apply` runs.

- Spec: [spec/tagherd CLI Spec.md](spec/tagherd%20CLI%20Spec.md)
- Component designs and backlog: linked at the end of the spec

## Status

Phases 1 and 2 (milestone 1) are implemented and run offline:

- Config, policy loading and validation, and the type catalog
- The evaluation engine: classification, group matching, every finding code,
  compliance summaries and `--fail-on` exit codes
- `scan`, against a fake-provider fixture until the cloud adapters land in
  phase 3

`inventory`, `plan` and `apply` are registered but report the phase they
arrive in.

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

# Until phase 3, scan runs against a fake-provider fixture
tagherd scan --fixture testdata/fixtures/scan.yaml
tagherd scan --fixture testdata/fixtures/scan.yaml --cloud azure --fail-on missing
tagherd scan --fixture testdata/fixtures/scan.yaml --summary -o markdown
```

`scan` exits 0 when there are no findings in the `--fail-on` classes, 1 when
there are, 2 on usage, config or policy errors, and 3 when a scope could not
be read. Golden output for every format lives in
`internal/cli/testdata/golden`; refresh it with
`go test ./internal/cli -update` after checking a change.

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
internal/engine/      classify, match and evaluate resources (pure)
internal/scan/        discovery across scopes, then the engine
internal/provider/    provider interfaces; fake/ for tests and fixtures
internal/output/      renderers and exit codes
internal/model/       shared types
policy/               the tagging policy
```
