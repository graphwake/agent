# Graphwake CI agent

`graphwake` sends your service's OpenAPI spec from your CI pipeline to Graphwake. Add one step to the pipeline;
it runs in pull request builds and in default-branch builds and works out which one it is on its own:

- **Pull request build:** the spec is stored as a draft and the teams that use the API hear about breaking
  changes before they ship.
- **Default-branch build (after a merge):** the spec becomes the live spec and the real notifications go out. If
  the commit came from a pull request, that pull request's draft notifications are settled as "merged".
- **Anything else** (feature branches, tags, unknown CI): nothing is sent.

The agent **never fails your pipeline**. The exit code is always 0. It prints exactly one line: what was sent, why
nothing was sent, or a warning in your CI's warning format. The ingest key is never printed.

The agent sends the spec as is (YAML or JSON); it does not generate it. See [Producing the spec](#producing-the-spec).

## Install

| Where | How |
|---|---|
| GitHub Actions | `uses: graphwake/agent/github-action@v0.1.0` ([snippet](#github-actions)) |
| GitLab CI | include `gitlab/graphwake.gitlab-ci.yml` ([snippet](#gitlab-ci)) |
| Azure DevOps | the `GraphwakeUpload` task ([snippet](#azure-devops)) |
| Jenkins, Bitbucket, anything else | the Docker image `ghcr.io/graphwake/agent:0.1.0` or the binary |

Binaries for Linux, macOS and Windows (amd64 and arm64) are on the
[releases page](https://github.com/graphwake/agent/releases), with a `checksums.txt`:

```sh
VERSION=0.1.0
BASE=https://github.com/graphwake/agent/releases/download/v$VERSION
ARCHIVE=graphwake_${VERSION}_linux_amd64.tar.gz
curl -fsSLO "$BASE/$ARCHIVE" && curl -fsSLO "$BASE/checksums.txt" &&
  sha256sum --check --ignore-missing checksums.txt &&
  tar -xzf "$ARCHIVE" graphwake &&
  ./graphwake upload --spec openapi.json || echo "graphwake: agent not run, spec not sent"
```

The `|| echo` keeps the step green if the download or the checksum check fails (the agent itself always exits
0). Every snippet below follows the same pattern: verify `checksums.txt` before running, never fail the build.

Each ingest key belongs to one service. For a repository with several services, run the step once per service,
each with its own spec and key.

## Configuration

Every setting can be given as a flag or as an environment variable. A flag wins over the variable.

| Flag | Variable | Meaning |
|---|---|---|
| `--spec` | `GRAPHWAKE_SPEC` | Spec file path, or an `http(s)` URL to read it from. Required (except when a pull request is closed). |
| `--key` | `GRAPHWAKE_KEY` | The service's ingest key (`dsk_...`). Use the variable from a CI secret; the flag is accepted but shows up in process lists. |
| `--url` | `GRAPHWAKE_URL` | Graphwake API URL. Default `https://api.graphwake.com`. |
| `--live-branch` | `GRAPHWAKE_LIVE_BRANCH` | Branches whose builds publish the live spec. Comma-separated, globs allowed: `main,release/*`. Default: the platform's default branch (GitLab, GitHub), else `main` and `master`. |
| `--live` | `GRAPHWAKE_LIVE=true` | Send as the live spec regardless of the detected context. |
| `--pr` | `GRAPHWAKE_PR` | Pull request number, for CIs the agent cannot read. |
| `--pr-url` | `GRAPHWAKE_PR_URL` | Pull request link. |
| `--author` | `GRAPHWAKE_AUTHOR` | Pull request author. |
| `--state` | `GRAPHWAKE_STATE` | `open` (default), `closed` or `merged`. |

`graphwake version` prints the version.

The old `DOCSYNC_*` names (`DOCSYNC_KEY`, `DOCSYNC_URL`, `DOCSYNC_SPEC`, ...) still work as fallbacks and are
**deprecated**; use `GRAPHWAKE_*`.

## How the build is classified

| Platform | Pull request build | Live build |
|---|---|---|
| GitHub Actions | `pull_request` / `pull_request_target` events. A PR closed without merging sends "closed" (no spec). A merged PR sends nothing: the default-branch build reports the merge. | `push` (or a manual/scheduled run) on a live branch |
| GitLab CI | merge request pipelines (`CI_MERGE_REQUEST_IID`) | `CI_COMMIT_BRANCH` is a live branch (default `CI_DEFAULT_BRANCH`) |
| Azure DevOps | `BUILD_REASON=PullRequest` | other builds of a live branch (default `main`/`master`) |
| Bitbucket Pipelines | `BITBUCKET_PR_ID` | `BITBUCKET_BRANCH` is a live branch |
| Jenkins (multibranch) | `CHANGE_ID`, `CHANGE_URL`, `CHANGE_AUTHOR` | `BRANCH_NAME` (or `GIT_BRANCH`) is a live branch |
| Anything else | `--pr` | only with `--live` |

With the spec the agent sends the build's context: the platform, the commit SHA and commit time, and the branch
the commit is on (a pull request's source branch, else the branch being built; none for tag builds). The branch
comes from `GITHUB_HEAD_REF` / `GITHUB_REF`, `CI_MERGE_REQUEST_SOURCE_BRANCH_NAME` / `CI_COMMIT_BRANCH` /
`CI_COMMIT_REF_NAME`, `SYSTEM_PULLREQUEST_SOURCEBRANCH` / `BUILD_SOURCEBRANCH`, `BITBUCKET_BRANCH`, and
`CHANGE_BRANCH` / `BRANCH_NAME` / `GIT_BRANCH`. A branch name longer than 255 characters is left out.

On a live build the agent:

1. **Checks the build is the branch tip.** If the branch has moved on (an old job re-run, two pipelines finishing
   out of order), nothing is sent and a newer build sends the spec. On GitHub it asks the API with the job token,
   elsewhere `git ls-remote origin <branch>`. If the check cannot be made (no git, no credentials, network), the
   spec is sent.
2. **Finds the merged pull request.** On GitHub through the API (`GET /repos/{repo}/commits/{sha}/pulls`, correct
   for merge, squash and rebase merges); otherwise, or if the API is unavailable, from the platform's default
   merge commit message: `Merge pull request #12`, squash `Title (#12)`, GitLab `See merge request group/project!12`,
   Azure `Merged PR 12:`, Bitbucket `(pull request #12)`. Found: the spec is sent as "pull request #12 merged".
   Not found: it is sent as live. Teams that write custom merge messages see the merge and the live change
   separately.

The agent also sends the CI platform, the commit SHA and the commit time, so Graphwake can ignore a spec from an
older commit than the live one.

## Platforms

### GitHub Actions

```yaml
on:
  pull_request:
    types: [opened, synchronize, reopened, closed]
  push:
    branches: [main]

jobs:
  api-spec:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      # ... build and produce openapi.json (see "Producing the spec")
      - uses: graphwake/agent/github-action@v0.1.0
        with:
          spec: openapi.json
          key: ${{ secrets.GRAPHWAKE_KEY }}
```

The `closed` type lets the agent report pull requests closed without merging. The action downloads the pinned
agent release for the runner's OS and architecture, verifies its SHA-256 against `checksums.txt` and runs it.
Inputs: `spec`, `key`, `url`, `live-branch`, `version`, `github-token` (defaults to the job token; it needs
`contents: read` and `pull-requests: read`, the defaults for most repositories).

Pull requests from forks get no secrets; the step then prints a warning and sends nothing.

### GitLab CI

```yaml
include:
  - remote: https://raw.githubusercontent.com/graphwake/agent/v0.1.0/gitlab/graphwake.gitlab-ci.yml

graphwake:
  extends: .graphwake-upload
  needs: [build]
  variables:
    GRAPHWAKE_SPEC: build/openapi.json
```

Add `GRAPHWAKE_KEY` as a masked CI/CD variable. The template runs in merge request pipelines and on the default
branch. The image has no git, so the tip-of-branch check is skipped there; the merged merge request is read from
`CI_COMMIT_MESSAGE`. To run it in your own image instead, download the binary as in [Install](#install).

### Azure DevOps

With the extension installed:

```yaml
steps:
  # ... build and produce openapi.json
  - task: GraphwakeUpload@0
    inputs:
      spec: openapi.json
      key: $(GRAPHWAKE_KEY)
```

`GRAPHWAKE_KEY` is a secret pipeline variable. Without the extension, run the binary from a script step:

```yaml
  - bash: |
      VERSION=0.1.0
      BASE=https://github.com/graphwake/agent/releases/download/v$VERSION
      ARCHIVE=graphwake_${VERSION}_linux_amd64.tar.gz
      curl -fsSLO "$BASE/$ARCHIVE" && curl -fsSLO "$BASE/checksums.txt" &&
        sha256sum --check --ignore-missing checksums.txt &&
        tar -xzf "$ARCHIVE" graphwake &&
        ./graphwake upload --spec openapi.json || echo "graphwake: agent not run, spec not sent"
    displayName: Upload spec to Graphwake
    continueOnError: true
    env:
      GRAPHWAKE_KEY: $(GRAPHWAKE_KEY)
```

Azure has no default-branch variable: the live branches are `main` and `master` unless `liveBranch` /
`GRAPHWAKE_LIVE_BRANCH` says otherwise. Pull requests closed without merging are not reported by Azure; their
drafts close on their own after 14 days.

### Jenkins

```groovy
stage('API spec') {
  steps {
    withCredentials([string(credentialsId: 'graphwake-key', variable: 'GRAPHWAKE_KEY')]) {
      sh 'docker run --rm -e GRAPHWAKE_KEY -e BRANCH_NAME -e CHANGE_ID -e CHANGE_URL -e CHANGE_AUTHOR -e GIT_COMMIT -e JENKINS_URL -v "$PWD:/w" -w /w ghcr.io/graphwake/agent:0.1.0 upload --spec openapi.json || true'
    }
  }
}
```

Multibranch pipelines give `CHANGE_ID` on pull request builds and `BRANCH_NAME` on branch builds. Or download the
binary with the checksum-verified script from [Install](#install) in an `sh` step (then git is available for the
tip check and the merge message). Pin the image by digest (`ghcr.io/graphwake/agent@sha256:...`) if you want the
same guarantee as the checksum.

### Bitbucket Pipelines

```yaml
definitions:
  steps:
    - step: &graphwake
        name: API spec
        script:
          - # ... produce openapi.json
          - >-
            VERSION=0.1.0; BASE=https://github.com/graphwake/agent/releases/download/v$VERSION;
            ARCHIVE=graphwake_${VERSION}_linux_amd64.tar.gz;
            (curl -fsSLO "$BASE/$ARCHIVE" && curl -fsSLO "$BASE/checksums.txt" &&
            sha256sum --check --ignore-missing checksums.txt && tar -xzf "$ARCHIVE" graphwake &&
            ./graphwake upload --spec openapi.json) || echo "graphwake: agent not run, spec not sent"
pipelines:
  pull-requests:
    '**':
      - step: *graphwake
  branches:
    main:
      - step: *graphwake
```

Add `GRAPHWAKE_KEY` as a secured repository variable.

### Any other CI

```sh
graphwake upload --spec openapi.json --pr "$PR_NUMBER" --pr-url "$PR_URL" || true   # pull request builds
graphwake upload --spec openapi.json --live || true                                 # release builds
```

## Output

| Line | Meaning |
|---|---|
| `graphwake: spec sent (pull request #42)` | Draft stored for the pull request. |
| `graphwake: spec sent (pull request #42 merged, live)` | The spec is live; the PR's notifications are settled. |
| `graphwake: spec sent (live)` | The spec is live. |
| `graphwake: pull request #42 closed, sent` | The PR's drafts are discarded. |
| `graphwake: not a pull request or live-branch build, nothing sent` | Feature branch, tag or unknown CI. |
| `graphwake: this build is not the tip of main ...` | A newer build will send. |
| warning `invalid or missing ingest key` | 401: check `GRAPHWAKE_KEY` (keys stop working when rotated). |
| warning `this service takes its spec from a URL, not from CI` | The service is set up to poll a URL. |
| warning `spec too large (limit 10 MB)` | The request body would be over 10 MB. |
| warning `spec rejected: ...` | The spec is not valid OpenAPI, or the service no longer exists. |
| warning `Graphwake unavailable, spec not sent` | Network error or 503 after 3 attempts. |

Warnings use `::warning::` on GitHub, `##vso[task.logissue type=warning]` on Azure DevOps and a `WARNING` prefix
elsewhere.

Reading the spec from a URL retries refused connections and 5xx answers for up to about a minute (5 attempts,
30 s per request), so the step can run right after the application starts.

## Producing the spec

The agent needs the spec as a file or a URL. Common recipes:

**ASP.NET Core (build time).** Add `Microsoft.Extensions.ApiDescription.Server`; the build writes the document:

```xml
<PackageReference Include="Microsoft.Extensions.ApiDescription.Server" Version="9.*" PrivateAssets="all" />
<PropertyGroup>
  <OpenApiDocumentsDirectory>$(MSBuildProjectDirectory)/openapi</OpenApiDocumentsDirectory>
</PropertyGroup>
```

With Swashbuckle: `dotnet tool install Swashbuckle.AspNetCore.Cli` then
`dotnet swagger tofile --output openapi.json bin/Release/net9.0/MyApi.dll v1`.

**Spring Boot.** `springdoc-openapi-maven-plugin` (or the Gradle plugin) starts the app during `integration-test`
and writes `target/openapi.json`:

```xml
<plugin>
  <groupId>org.springdoc</groupId>
  <artifactId>springdoc-openapi-maven-plugin</artifactId>
  <executions><execution><id>integration-test</id><goals><goal>generate</goal></goals></execution></executions>
</plugin>
```

**FastAPI.**

```sh
python -c "import json; from app.main import app; print(json.dumps(app.openapi()))" > openapi.json
```

**NestJS.** In a small script, build the document with `SwaggerModule.createDocument(app, config)` and write it
with `fs.writeFileSync('openapi.json', JSON.stringify(document))`, then exit without `app.listen()`.

**Go.** [`swag`](https://github.com/swaggo/swag): `swag init -o docs` writes `docs/swagger.json` (Swagger 2.0,
accepted).

**Any stack: start the app and read the URL.**

```sh
./start-my-api &           # or docker run -d -p 5000:8080 my-api
graphwake upload --spec http://localhost:5000/swagger/v1/swagger.json
```

## Building and releasing

```sh
go vet ./... && go test ./...
node --test azure-devops/GraphwakeUpload/   # the Azure task's download (Node 20+)
go build -o graphwake ./cmd/graphwake
```

Standard library only. Releases are made with GoReleaser (`.goreleaser.yaml`): binaries, `checksums.txt` and a
multi-arch image `ghcr.io/graphwake/agent` (distroless, with a busybox shell for GitLab and Jenkins). The release
workflow is in `release/github-workflow.yml`; copy it to `.github/workflows/` of the repository that publishes the
releases (instructions at the top of the file) and push a tag `vX.Y.Z`. The GitHub Action, the GitLab template and
the Azure task download from `github.com/graphwake/agent`; if releases are published from another repository, a
token with access to it is needed instead of the default `GITHUB_TOKEN`. The Azure DevOps extension is packaged
from `azure-devops/` with `tfx extension create --manifest-globs vss-extension.json` (publisher `graphwake` is a
placeholder).

The default API address (`https://api.graphwake.com`, the production domain) lives
in one place: `DefaultAPIURL` in `internal/cli/config.go`.
