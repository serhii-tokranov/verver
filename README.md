# Verver

Verver assigns automatic version tags after successful GitHub Actions checks. The main branch receives stable versions, while other branches receive numbered release candidates.

```text
main       feat: initial code       v0.0.1
feature-a  feat: add search         v0.0.2-rc.1
feature-b  fix: validate input      v0.0.2-rc.2
feature-a  verver: bump minor       v0.1.0-rc.1
feature-a  feat: finish search      v0.1.0-rc.2
main       merge feature-a         v0.1.0
```

Every successful tested push receives one assignment. A retry for the same branch and commit reuses that assignment. Failed and canceled checks create no version.

## Version bumps

Patch is the default, regardless of the conventional commit type. Minor, major, and exact versions use dedicated empty commits:

```sh
git commit --allow-empty -m "verver: bump minor"
git commit --allow-empty -m "verver: bump major"
git commit --allow-empty -m "verver: bump v1.4.0"
```

The complete trimmed commit message must match one of these forms. A bump command on a commit containing file changes, a merge commit, or a root commit is rejected.

The latest bump commit in the unreleased change wins. This makes corrections explicit:

```text
verver: bump major
verver: bump minor    effective instruction
```

An exact version must match `main-pattern`, including its configured prefix:

| `main-pattern` | Exact bump command |
| --- | --- |
| `vMAJOR.MINOR.PATCH` | `verver: bump v1.4.0` |
| `release-MAJOR.MINOR.PATCH` | `verver: bump release-1.4.0` |
| `MAJOR.MINOR.PATCH` | `verver: bump 1.4.0` |

Exact targets must be greater than the latest stable version. Existing tags are never moved or replaced. If another release reaches the target first, Verver fails and requires a new bump commit.

The intent stays active for later pushes on the branch. Relative minor and major requests are recalculated from the latest stable version when main advances. Previously assigned RC tags remain historical candidates.

## GitHub Actions

Verver separates testing from tag creation. Project CI runs with read-only repository access. A second workflow, loaded from the default branch after CI succeeds, receives write access only for finalization. The finalizer reads Git history but never executes code, actions, caches, or artifacts from the tested revision.

Keep the repository or organization default `GITHUB_TOKEN` permission read-only. Add your normal CI workflow at `.github/workflows/ci.yml`:

```yaml
name: CI

on:
  push:
    branches: ['**']
  pull_request:

permissions:
  contents: read

jobs:
  checks:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - run: ./your-checks
```

Then add `.github/workflows/version.yml`:

```yaml
name: Version

on:
  workflow_run:
    workflows: [CI]
    types: [completed]

permissions:
  contents: read

jobs:
  version:
    if: >-
      github.event.workflow_run.event == 'push' &&
      github.event.workflow_run.conclusion == 'success' &&
      github.event.workflow_run.path == '.github/workflows/ci.yml' &&
      github.event.workflow_run.head_repository.full_name == github.repository
    runs-on: ubuntu-latest
    permissions:
      contents: write
      pull-requests: read
    concurrency:
      group: verver-release
      queue: max
      cancel-in-progress: false
    steps:
      - uses: serhii-tokranov/verver@v0.2.0
        with:
          ref: refs/heads/${{ github.event.workflow_run.head_branch }}
          sha: ${{ github.event.workflow_run.head_sha }}
```

`contents: write` is required because GitHub does not provide a tags-only token permission. `pull-requests: read` lets Verver recover original ordered commits after squash and rebase merges. Protect changes to the finalizer workflow through normal default-branch review and repository rules.

The readable release tag above is the normal installation. Pin Verver to the corresponding full commit SHA when repository policy requires an immutable dependency reference.

If the CI file has another path, configure both the condition and Action input:

```yaml
      github.event.workflow_run.path == '.github/workflows/build.yml'

# ...
        with:
          ref: refs/heads/${{ github.event.workflow_run.head_branch }}
          sha: ${{ github.event.workflow_run.head_sha }}
          workflow: .github/workflows/build.yml
```

The workflow name in `workflows: [CI]` must match the source workflow's top-level `name`.

### Configuration

| Input | Default | Meaning |
| --- | --- | --- |
| `ref` | required | Full tested branch ref |
| `sha` | required | Full tested commit SHA |
| `workflow` | `.github/workflows/ci.yml` | Trusted source workflow path |
| `main-branch` | `main` | Stable release branch |
| `main-pattern` | `vMAJOR.MINOR.PATCH` | Literal prefix plus numeric version |
| `feature-pattern` | `-rc.RC` | Candidate label and shared counter |
| `token` | `github.token` | Repository token used by the finalizer |
| `adopt-existing` | `false` | Accept matching tags not created by Verver |

For example:

```yaml
- uses: serhii-tokranov/verver@v0.2.0
  with:
    ref: refs/heads/${{ github.event.workflow_run.head_branch }}
    sha: ${{ github.event.workflow_run.head_sha }}
    main-pattern: release-MAJOR.MINOR.PATCH
    feature-pattern: -preview.RC
```

Outputs are `tag`, `sha`, `kind` (`stable` or `rc`), and `status` (`created` or `reused`). Add `id: version` to read values such as `steps.version.outputs.tag`.

## Release behavior

- The initial baseline is `0.0.0`; the first normal main release is `v0.0.1`.
- Ordinary feature work targets the next patch of the latest stable release.
- RC counters start at one and are shared across branches for the same numeric target.
- Merging to main creates one stable version for the successful tested push.
- Squash, rebase, and merge strategies are supported. Verver retrieves the original ordered PR commits instead of relying on PR titles or final merge messages.
- A stable release records consumed original commit IDs so old intent is not applied again.
- An assignment belongs to the full branch ref and tested commit. The same commit can have separate feature and main assignments.

The shared concurrency group must cover every Verver finalizer in the repository. Assignment order follows finalization order, not push timestamps. An old main run overtaken by a stable release fails as stale; a retry of an existing assignment still succeeds.

Tags are annotated, immutable, and point to the tested commit. If a connection drops after Git accepts a tag, a retry discovers the assignment instead of creating another version.

## CLI

Build with Go 1.26 or later:

```sh
go build -trimpath -o bin/verver ./cmd/verver
```

Preview from complete local history without network access or writes:

```sh
bin/verver next --ref refs/heads/my-feature \
  --main-ref refs/remotes/origin/main
```

To assign a tag outside GitHub Actions, provide the tested ref and SHA after all checks succeed while holding a repository-wide writer lock:

```sh
VERVER_TOKEN=... bin/verver release \
  --ref refs/heads/my-feature \
  --sha FULL_TESTED_COMMIT_SHA \
  --serialized --format json
```

The CLI uses `origin` by default and HTTPS for remote writes. It uses `go-git`; no Git subprocesses or hooks are executed. GitHub Actions release additionally validates the completed source workflow, repository, branch, SHA, event, and conclusion.

Run `verver COMMAND --help` for all options. Exit codes are `0` for success, `2` for invalid arguments, and `1` for repository, policy, network, or output failures.

## Supported scope

- Three numeric components with an optional literal prefix.
- Candidate suffixes such as `-rc.1`, `-beta.2`, and `-preview.3`.
- Complete ordinary or bare SHA-1 repositories, detached checkouts, packed refs, and annotated or lightweight historical tags.
- Linux GitHub-hosted runners. Local Go tests also run on macOS.

Shallow and partial clones, linked worktrees, alternate hash or ref formats, automatic pattern migration, multiple independent package streams, and maintenance release lines are unsupported. Existing matching unmanaged tags require `adopt-existing: true`; changing a managed pattern or main branch requires an explicit migration.

No telemetry is collected.

## Development

```sh
go test -race ./...
go vet ./...
go build ./...
```

## License

[MIT](LICENSE) © 2026 Serhii Tokranov.
