# Verver

Automatic version tags for Git pipelines. Main receives stable releases; feature branches receive numbered release candidates. Every successful push gets one assignment, and retrying the same branch and commit reuses it.

```text
main       feat: initial code                  v0.0.1
feature-a  feat: add search                    v0.0.2-rc.1
feature-b  fix: improve validation             v0.0.2-rc.2
feature-a  chore: [version:minor]              v0.1.0-rc.1
main       merge feature-b                     v0.0.2
main       squash feature-a [version:minor]    v0.1.0
feature-c  docs: update examples               v0.1.1-rc.1
```

## Version requests

Patch is the default—even for `feat:` commits. Include a version marker anywhere in a commit message to request another target:

| Marker | From `v1.2.3` |
| --- | --- |
| No marker | `v1.2.4` |
| `[version:minor]` | `v1.3.0` |
| `[version:major]` | `v2.0.0` |
| `[version:set=2.5.0]` | `v2.5.0` |

Major wins over minor; minor resets patch, and major resets both minor and patch.

### Exact versions

Use an exact version for migrations, coordinated releases, or aligning an existing project:

```text
chore: [version:set=2.5.0] align release version
```

On a feature branch, Verver creates candidates such as `v2.5.0-rc.1`. When the change reaches main, it creates `v2.5.0`. Specify only the numeric `MAJOR.MINOR.PATCH` value; configured patterns add the prefix and candidate suffix.

The requested version must be greater than the latest stable version. Existing tags are never replaced or moved. Different exact versions in the same pending history are an error, as is combining an exact marker with a minor or major marker. The request remains active until released, and squash merges must preserve it in the final commit message. If another release reaches or passes the requested version first, the pending exact request fails.

Verver does not support downgrades. To restore older code, revert it and publish a new higher version. Maintenance releases for older major versions require separate release streams, which are not currently supported.

## GitHub Actions

Run Verver in a **final job that depends on every required check**. That job needs a full checkout, permission to create tags, and a shared concurrency group across all branches and workflows using Verver.

Add this job to an existing workflow triggered by branch pushes. Replace `checks` with your required job IDs. The example uses an exact release tag; update it deliberately when upgrading. Pin the corresponding full commit SHA instead when your supply-chain policy requires it.

```yaml
version:
  if: github.event_name == 'push' && !github.event.deleted
  needs: [checks]
  runs-on: ubuntu-latest
  permissions:
    contents: write
    pull-requests: read
  concurrency:
    group: verver-release
    queue: max
  steps:
    - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      with:
        fetch-depth: 0
        persist-credentials: false
    - uses: serhii-tokranov/verver@v0.0.3
```

The Action builds its pinned source with Go; no Docker image or separate binary installation is needed. It uses `github.token` by default. Repository or organization tag rules must allow that token to create the selected tags. PR and merge-queue events never assign versions, and branch-push triggers avoid tag-triggered loops.

This repository uses the same pinned Action in its own [CI workflow](.github/workflows/ci.yml). It runs formatting, tests, vet, build, and workflow lint before assigning versions. Pull-request intent validation separately builds its checker from trusted main code.

### Configuration

All inputs are optional:

| Input | Default | Meaning |
| --- | --- | --- |
| `main-branch` | `main` | Stable release branch |
| `main-pattern` | `vMAJOR.MINOR.PATCH` | Literal prefix plus the numeric version |
| `feature-pattern` | `-rc.RC` | Candidate label and shared counter |
| `path` | `.` | Full checkout of the tested repository |
| `token` | `github.token` | Contents write and pull requests read |
| `adopt-existing` | `false` | Explicitly accept existing matching version tags |

For example:

```yaml

- uses: serhii-tokranov/verver@v0.0.3
  with:
    main-pattern: 'release-MAJOR.MINOR.PATCH'
    feature-pattern: '-preview.RC'
```

Outputs are `tag`, `sha`, `kind` (`stable` or `rc`), and `status` (`created` or `reused`). Add `id: version` to the Action step to read `steps.version.outputs.tag`, for example. The final tag is assigned after checks, so it is unavailable to earlier build steps.

## Release behavior

- The initial baseline is `0.0.0`. A normal first main push produces `v0.0.1`.
- Features target the next patch of the latest stable release unless their unreleased commits request minor, major, or an exact higher version.
- RC counters start at one and are shared across branches **for the same numeric target**. A larger counter means a later assignment, not that it contains another branch's code.
- A pending bump persists until its changes are released. It is not reapplied on every feature push. If main overtakes the target, the next feature push applies the pending intent against the new baseline.
- Merging to main creates a stable version. GitHub merge provenance records which original feature commits were consumed, including squash and rebase merges. Their IDs remain in the stable tag even if old PR objects are later pruned.
- Keep the bump marker in the final squash message. If landed history loses a required marker, finalization fails without a tag; add a corrective commit carrying the marker and rerun checks.
- An assignment belongs to a full branch ref and commit. Another workflow run or a recreated branch at the same commit reuses it. Feature and main assignments remain distinct, even for the same SHA.

Before squashing, `verver check-pr` verifies that the PR title or body contains the required marker. Main finalization checks the actual landed commits too. For merge/rebase workflows, pass `--merge-method merge` or `--merge-method rebase` to the PR check.

### Failures and concurrency

Failed required checks skip the version job. Tags are immutable and point to the tested commit. If the connection drops after Git accepts a tag, a retry discovers it instead of assigning another version.

The shared CI lock must cover the entire finalization operation. `--serialized` acknowledges that requirement; it does not create a distributed lock. Uncoordinated writers are unsupported. [GitHub's `queue: max`](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency) holds up to 100 pending jobs; overflow can be canceled. Assignment order follows finalization, not push timestamps. An older main run overtaken by a stable release fails as stale; an already assigned retry still succeeds.

Cancellation after Git has accepted a tag cannot undo it. Do not delete accepted tags to retry a failed output or cleanup step.

## CLI

Build with Go 1.26 or later:

```sh
go build -trimpath -o bin/verver ./cmd/verver
```

Preview from local history without network access or writes:

```sh
bin/verver next --ref refs/heads/my-feature \
  --main-ref refs/remotes/origin/main
```

A preview is provisional: it cannot prove remote freshness or discover new rewritten merges. To assign a tag after successful checks, while holding a shared CI lock:

```sh
# Set VERVER_TOKEN or GITHUB_TOKEN through your CI secret store.
bin/verver release --ref refs/heads/my-feature \
  --sha FULL_TESTED_COMMIT_SHA --serialized --format json
```

The CLI uses `origin` by default. Use `--remote-url https://github.com/OWNER/REPO.git` if the configured origin uses SSH. GitHub.com provenance is detected automatically from the HTTPS URL. `--github owner/repo` selects it explicitly; other Git hosts require preserved ancestry or a verified `--provenance` JSON file for rewritten merges:

```json
[{"head":"FULL_ORIGINAL_MERGED_HEAD_SHA"}]
```

Other integrations are responsible for supplying complete provenance. Verver cannot infer a custom squash from Git ancestry alone. On GitHub Actions, release also verifies the push event's branch and SHA. Tokens are read from the environment, never command-line arguments.

Run `verver COMMAND --help` for options. Exit codes: `0` success, `2` invalid arguments, `1` repository, policy, network, or output failure.

## Supported scope

- Three numeric components, optionally prefixed: `1.2.3`, `v1.2.3`, `release-1.2.3`, `api/v1.2.3`.
- Candidate suffixes such as `-rc.1`, `-beta.2`, or `-preview.3`. Labels begin with a lowercase letter and contain lowercase letters, digits, or hyphens.
- Canonical decimal numbers without leading zeros, up to unsigned 64-bit limits. Overflow is an error.
- Complete ordinary or bare SHA-1 repositories, detached checkouts, loose/packed refs, and annotated/lightweight historical tags. HTTPS remote operations use `go-git`; no Git subprocesses or hooks are executed by the CLI.
- Linux GitHub-hosted runners are the initial Action target. Local Go tests also run on macOS. GitHub Enterprise requires compatible API/concurrency support and has not been validated.

Shallow/partial clones, linked worktrees, alternate hash/ref formats, and automatic pattern migrations are unsupported. Existing matching unmanaged tags require `--adopt-existing`; they establish numeric history but cannot prove retry identity. Changing a managed pattern or main branch fails instead of resetting the counter. Keep Verver tags and their annotations intact.

Calendar versions, hashes/metadata in version strings, multiple packages, standalone distributed locking, and prebuilt binary distribution are future work. No telemetry is collected.

## Development

```sh
go test -race ./...
go vet ./...
go build ./...
```

Tests include release scenarios, real repository fixtures, in-process Git pack transfer and tag creation, GitHub API fixtures, and CLI release/rerun behavior. They do not need credentials or a running Git server. Hosted workflow permissions and merge behavior still need verification in the destination GitHub repository.

## License

[MIT](LICENSE) © 2026 Serhii Tokranov.
