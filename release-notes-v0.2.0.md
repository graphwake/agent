## Graphwake CI agent v0.2.0

Pull request comments and an optional check. On an open pull request build on GitHub Actions, or on Azure Pipelines with an Azure Repos repository, the agent now writes the impact of the change on the pull request itself: the breaking changes and the teams subscribed to the endpoints they touch. With `--fail-on` it can also fail the build until the breaking change is fixed or accepted.

The agent still **never fails your pipeline unless you turn on `--fail-on`**. Without it the exit code is always 0. With it, the only non-zero exit is 1, on an open pull request build whose check failed. Graphwake being down, slow or rejecting the request, a comment that cannot be written and an unsupported CI are warnings, never failures.

### Highlights

- **Pull request comment** (GitHub and Azure Repos, on by default): one comment per service, updated on every build instead of adding new ones. It lists up to 10 breaking changes (`METHOD /path: what changed`), the affected teams, the changed endpoints with no consumer mapped yet, the number of non-breaking changes and a link to the impact in Graphwake. A pull request that never had a breaking change gets no comment; when a later build has none, the comment changes to "No breaking changes anymore." (on Azure DevOps the thread is resolved). Turn it off with `--no-comment` or `GRAPHWAKE_COMMENT=false`.
- **Team names follow the repository's visibility:** shown in private and internal repositories, only the count in public ones. Each service can change this in Graphwake ("Team names in pull request comments": Automatic, Always show, Never show). Alert channel names are never shown.
- **Optional check, `--fail-on breaking|affected`** (`GRAPHWAKE_FAIL_ON`): `breaking` fails the pull request build on any breaking change, `affected` (recommended) only when a breaking change affects at least one subscribed team. Off by default. Default-branch builds never fail.
- **Escape hatch:** the label `graphwake-accept-breaking` on the pull request lets a failing check pass. Labels are read when the agent runs, so re-running the check after adding the label is enough.
- **GitHub Action:** new inputs `fail-on` and `comment`; the action fails the step only when the agent exits 1. The workflow needs `pull-requests: write` to comment.
- **Azure DevOps task `GraphwakeUpload@0`** (0.2.0): new inputs `failOn` and `comment`; the task hands the build's own token to the agent and fails the step only when the agent exits 1. The `<Project> Build Service` identity needs "Contribute to pull requests: Allow" on the repository.
- **Bounded:** all GitHub and Azure DevOps API calls of a run share a 10-second budget; the CI token is never printed.

### Install

**GitHub Actions**

```yaml
on:
  pull_request:
    types: [opened, synchronize, reopened, closed, labeled, unlabeled]
  push:
    branches: [main]

permissions:
  contents: read
  pull-requests: write

jobs:
  api-spec:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: graphwake/agent/github-action@v0.2.0
        with:
          spec: openapi.json
          key: ${{ secrets.GRAPHWAKE_KEY }}
          # fail-on: affected   # optional
```

**Azure DevOps**

```yaml
- task: GraphwakeUpload@0
  inputs:
    spec: openapi.json
    key: $(GRAPHWAKE_KEY)
    # failOn: affected   # optional
```

In a script step instead of the task, map the build's token so the agent can comment: `SYSTEM_ACCESSTOKEN: $(System.AccessToken)` under `env`.

**GitLab CI**

```yaml
include:
  - remote: https://raw.githubusercontent.com/graphwake/agent/v0.2.0/gitlab/graphwake.gitlab-ci.yml

graphwake:
  extends: .graphwake-upload
  variables:
    GRAPHWAKE_SPEC: openapi.json
```

**Docker (Jenkins, Bitbucket, anything else)**

Pass your CI's environment variables through with `-e` so the agent can detect the build (see the README for the list for each platform):

```sh
docker run --rm -e GRAPHWAKE_KEY -v "$PWD:/w" -w /w ghcr.io/graphwake/agent:0.2.0 upload --spec openapi.json
```

**Binary**

```sh
VERSION=0.2.0
BASE=https://github.com/graphwake/agent/releases/download/v$VERSION
ARCHIVE=graphwake_${VERSION}_linux_amd64.tar.gz
curl -fsSLO "$BASE/$ARCHIVE" && curl -fsSLO "$BASE/checksums.txt" &&
  sha256sum --check --ignore-missing checksums.txt &&
  tar -xzf "$ARCHIVE" graphwake &&
  ./graphwake upload --spec openapi.json || echo "graphwake: agent not run, spec not sent"
```

See the [README](https://github.com/graphwake/agent#readme) for the comment, the check, the permissions and every option.

### Assets

| Platform | amd64 | arm64 |
|---|---|---|
| Linux | `graphwake_0.2.0_linux_amd64.tar.gz` | `graphwake_0.2.0_linux_arm64.tar.gz` |
| macOS | `graphwake_0.2.0_darwin_amd64.tar.gz` | `graphwake_0.2.0_darwin_arm64.tar.gz` |
| Windows | `graphwake_0.2.0_windows_amd64.zip` | `graphwake_0.2.0_windows_arm64.zip` |

SHA-256 checksums are in `checksums.txt`. The multi-arch Docker image `ghcr.io/graphwake/agent:0.2.0` (also `:latest`) is published for `linux/amd64` and `linux/arm64`.

### Notes

- **Comments and `--fail-on` work on GitHub Actions and on Azure Pipelines with Azure Repos only.** GitLab CI, Bitbucket Pipelines, Jenkins, other CIs and Azure Pipelines building a GitHub or Bitbucket repository get no comment yet, and `--fail-on` is not applied there: the agent warns `--fail-on is not supported on this CI yet; not failing` and exits 0.
- **Without the CI token** (`GITHUB_TOKEN`, or `SYSTEM_ACCESSTOKEN` on Azure DevOps) the agent writes no comment and warns; `--fail-on` still applies, but the label cannot be read. The GitHub Action and the Azure DevOps task pass the token for you.
- **Using `--fail-on` in your own script step:** remove `|| true` / `|| echo` after the agent and `continueOnError: true`, so exit 1 reaches the pipeline. The README has an Azure DevOps script step written this way.
- **Azure DevOps task version:** the task stays on major version 0 (`GraphwakeUpload@0`), so pipelines on `@0` get the comment once the extension is updated. Once the extension is public, behaviour changes will ship as a new major version.
- Needs a Graphwake server that returns the pull request outcome. With an older server the agent sends the spec as before and, if `--fail-on` is set, warns `the server did not return a check result; not failing`.
