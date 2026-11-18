# Graphwake CI agent

`graphwake` sends your service's OpenAPI spec (or AsyncAPI document, see [AsyncAPI documents](#asyncapi-documents-kafka-azure-service-bus))
from your CI pipeline to Graphwake. Add one step to the pipeline;
it runs in pull request builds and in default-branch builds and works out which one it is on its own:

- **Pull request build:** the spec is stored as a draft and the teams that use the API hear about breaking
  changes before they ship. On GitHub and Azure Repos the agent also comments on the pull request with the breaking
  changes and the affected teams, and can fail the build if you ask it to
  ([Pull request comments and the optional check](#pull-request-comments-and-the-optional-check)).
- **Default-branch build (after a merge):** the spec becomes the live spec and the real notifications go out. If
  the commit came from a pull request, that pull request's draft notifications are settled as "merged".
- **Anything else** (feature branches, tags, unknown CI): nothing is sent.

The agent **never fails your pipeline unless you turn on `--fail-on`**. Without it the exit code is always 0. It
prints exactly one line: what was sent, why nothing was sent, or a warning in your CI's warning format. The ingest
key and the CI token are never printed.

The agent sends the spec as is (YAML or JSON); it does not generate it. See [Producing the spec](#producing-the-spec).

Try the check in your browser first: https://graphwake.com/tools/openapi-diff

## Install

| Where | How |
|---|---|
| GitHub Actions | `uses: graphwake/agent/github-action@v0.2.0` ([snippet](#github-actions)) |
| GitLab CI | include `gitlab/graphwake.gitlab-ci.yml` ([snippet](#gitlab-ci)) |
| Azure DevOps | the `GraphwakeUpload` task ([snippet](#azure-devops)) |
| Jenkins, Bitbucket, anything else | the Docker image `ghcr.io/graphwake/agent:0.2.0` or the binary |

Binaries for Linux, macOS and Windows (amd64 and arm64) are on the
[releases page](https://github.com/graphwake/agent/releases), with a `checksums.txt`:

```sh
VERSION=0.2.0
BASE=https://github.com/graphwake/agent/releases/download/v$VERSION
ARCHIVE=graphwake_${VERSION}_linux_amd64.tar.gz
curl -fsSLO "$BASE/$ARCHIVE" && curl -fsSLO "$BASE/checksums.txt" &&
  sha256sum --check --ignore-missing checksums.txt &&
  tar -xzf "$ARCHIVE" graphwake &&
  ./graphwake upload --spec openapi.json || echo "graphwake: agent not run, spec not sent"
```

The `|| echo` keeps the step green if the download or the checksum check fails (the agent itself exits 0 unless you
turn on `--fail-on`). Every snippet below follows the same pattern: verify `checksums.txt` before running, never
fail the build. To let `--fail-on` fail it, see [Letting the check fail the build](#letting-the-check-fail-the-build).

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
| `--fail-on` | `GRAPHWAKE_FAIL_ON` | Fail an open pull request build on breaking changes: `breaking` (any breaking change) or `affected` (only when a breaking change affects a subscribed team; recommended). Default: never fail. GitHub and Azure Repos only. See [the check](#pull-request-comments-and-the-optional-check). |
| `--no-comment` | `GRAPHWAKE_COMMENT=false` | Do not comment on the pull request. |

`graphwake version` prints the version.

## AsyncAPI documents (Kafka, Azure Service Bus)

A service added in Graphwake with the contract **AsyncAPI** (Pro and Enterprise plans) takes an AsyncAPI 2.6 or 3.x
document (YAML or JSON) instead of an OpenAPI spec. The command, the flags and every CI snippet below stay the same; only
the file changes:

```sh
graphwake upload --spec asyncapi.yaml
```

The document must be one file: references to other files or URLs are rejected, so bundle it first (for example
`asyncapi bundle`). An OpenAPI spec sent to an AsyncAPI service, or the other way round, is rejected
(`spec rejected: This service expects an AsyncAPI document.`). If the workspace's plan does not include event contracts,
the upload is refused with HTTP 403 and the agent prints the warning `unexpected response 403, spec not sent` (exit 0).

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

## Pull request comments and the optional check

On an open pull request build on **GitHub Actions**, or on **Azure Pipelines building an Azure Repos repository**,
the agent comments on the pull request and, with `--fail-on`, can fail the build. GitLab, Bitbucket, Jenkins and
other CIs get no comment yet; see [Where it works](#where-it-works).

### The comment

One comment per service (a repository that sends several specs gets one comment for each). It shows:

- the title: `Graphwake · <service> · Impact <score> (<Low|Medium|High>)`;
- the breaking changes, up to 10 lines of `METHOD /path: what changed`, then "+N more";
- the affected teams: the teams subscribed to a changed endpoint, up to 10 names, then "+N more";
- how many changed endpoints have no consumer mapped yet, and how many non-breaking changes there are;
- the check line, when `--fail-on` is set;
- a link to the impact in Graphwake.

Every build of the pull request updates the same comment; it is found by a hidden marker, so the agent never adds a
second one. A pull request that never had a breaking change gets no comment. If an earlier build had breaking
changes and this one has none, the comment changes to "No breaking changes anymore." (on Azure DevOps the thread is
also resolved). Closed pull requests and default-branch builds leave the comment as it is.

**Team names.** In private and internal repositories the comment names the affected teams. In public repositories,
or when the visibility cannot be read, it gives only the count: "Affected teams: 3 (team names are hidden in public
repositories)". Each service can change this in Graphwake (service settings, "Team names in pull request comments":
Automatic, Always show or Never show). Alert channel names are never shown. The visibility comes from the event
payload on GitHub and from the project's visibility on Azure DevOps.

`--no-comment` (or `comment: false` in the GitHub Action and the Azure DevOps task) turns the comment off; the check
still works.

### The check (`--fail-on`)

| `--fail-on` | The pull request build fails when |
|---|---|
| not set (default) | never |
| `breaking` | the pull request has any breaking change |
| `affected` | a breaking change affects at least one subscribed team (recommended) |

"Affected team" means the same as on Graphwake's impact page: a team subscribed to an endpoint with a breaking
change. People who only follow an endpoint do not count. With `affected` and breaking changes that no subscribed
team uses, the build passes and the comment says "Not failing: no subscribed team is affected."

The check runs only on open pull request builds. Default-branch builds, merges and closed pull requests never fail,
whatever `--fail-on` says. An unknown value never fails either: the agent prints a warning.

**Accepting a breaking change.** If the change is intended, add the label `graphwake-accept-breaking` to the pull
request (any letter case) and run the check again: the build passes and the comment says the breaking changes were
accepted with the label. The agent reads the labels when it runs, not from the event that started the build, so a
re-run sees a label added later.

- GitHub: with `labeled` and `unlabeled` in the `pull_request` trigger types (as in the
  [snippet](#github-actions)), adding or removing the label runs the workflow again on its own. Without them,
  re-run the job by hand.
- Azure DevOps: adding a label does not start a build. Queue the pull request build again.

### Exit codes

| Exit code | When |
|---|---|
| 1 | Only with `--fail-on`, on an open pull request build on GitHub or Azure Repos, when Graphwake's check result is "failed". The comment is written first. |
| 0 | Everything else: the spec was sent, nothing was sent, any warning. |

These never fail the build, with or without `--fail-on`; each is a warning:

- Graphwake cannot be reached, times out, or rejects the request (invalid key, account closed, invalid spec).
- The comment cannot be written (missing permission, a pull request from a fork, a platform API error). The check
  still applies.
- The labels cannot be read. The check still applies, without the label.
- The calls to the GitHub or Azure DevOps API take longer than 10 seconds in total. The check still applies.

**Without the CI token.** The agent comments and reads labels with `GITHUB_TOKEN` on GitHub and `SYSTEM_ACCESSTOKEN`
on Azure DevOps. If the token is missing, the agent writes no comment and warns `no GITHUB_TOKEN: cannot comment on
the pull request or read its labels` (on Azure: `no SYSTEM_ACCESSTOKEN: ...`). `--fail-on` still applies, but the
label cannot be seen, so a failed check cannot be accepted. The GitHub Action passes the job token for you; on Azure
DevOps, map `SYSTEM_ACCESSTOKEN: $(System.AccessToken)` in the step's `env` (task, script or Docker), as the
[Azure DevOps](#azure-devops) section shows.

### Where it works

| CI | Comment | `--fail-on` |
|---|---|---|
| GitHub Actions | yes | yes |
| Azure Pipelines, Azure Repos repository | yes | yes |
| Azure Pipelines building a repository not hosted in Azure Repos (GitHub, Bitbucket or another Git host) | no | not applied |
| GitLab CI, Bitbucket Pipelines, Jenkins, any other CI | not yet | not applied |

Where `--fail-on` is not applied, the agent warns `--fail-on is not supported on this CI yet; not failing` and exits 0.

### Letting the check fail the build

The step has to pass the agent's exit code through. The GitHub Action and the Azure DevOps task do: they fail only
when the agent exits 1 and stay green on everything else. In your own script steps, remove `|| true` or `|| echo ...`
after the agent and `continueOnError: true`, and keep the download guarded so that a failed download still does not
fail the build. The [Azure DevOps](#azure-devops) section has a script step written this way.

## Platforms

### GitHub Actions

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
      # ... build and produce openapi.json (see "Producing the spec")
      - uses: graphwake/agent/github-action@v0.2.0
        with:
          spec: openapi.json
          key: ${{ secrets.GRAPHWAKE_KEY }}
          # fail-on: affected   # optional: fail the pull request on breaking changes that affect a subscribed team
```

The `closed` type lets the agent report pull requests closed without merging; `labeled` and `unlabeled` run the
check again when the label `graphwake-accept-breaking` is added or removed. The action downloads the pinned agent
release for the runner's OS and architecture, verifies its SHA-256 against `checksums.txt` and runs it. It fails
the step only when the agent exits 1 (a failed check with `fail-on` set).

Inputs: `spec`, `key`, `url`, `live-branch`, `fail-on` (empty, `breaking` or `affected`; default empty, never
fails), `comment` (default `true`), `version`, `github-token` (defaults to the job token).

Permissions: the branch tip check needs `contents: read`; the pull request comment, the labels and finding the
merged pull request need `pull-requests: write` (which includes read). Without `pull-requests: write` the agent
cannot comment and prints a warning. To publish from more branches, set `live-branch` and add the same branches to
the `push` trigger.

Pull requests from forks get no secrets; the step then prints a warning and sends nothing. In a private repository
that sends secrets to fork pull requests, the spec is sent but the job token stays read-only (unless write tokens
are sent too), so the comment fails with a warning.

### GitLab CI

```yaml
include:
  - remote: https://raw.githubusercontent.com/graphwake/agent/v0.2.0/gitlab/graphwake.gitlab-ci.yml

graphwake:
  extends: .graphwake-upload
  needs: [build]
  variables:
    GRAPHWAKE_SPEC: build/openapi.json
```

Add `GRAPHWAKE_KEY` as a masked CI/CD variable. The template runs in merge request pipelines and on the default
branch. The image has no git, so the tip-of-branch check is skipped there; the merged merge request is read from
`CI_COMMIT_MESSAGE`. To run it in your own image instead, download the binary as in [Install](#install).

No merge request comments on GitLab yet, and `--fail-on` is not applied there. The template never fails the
pipeline.

### Azure DevOps

With the extension installed:

```yaml
steps:
  # ... build and produce openapi.json
  - task: GraphwakeUpload@0
    inputs:
      spec: openapi.json
      key: $(GRAPHWAKE_KEY)
      # failOn: affected   # optional: fail the pull request on breaking changes that affect a subscribed team
    env:
      SYSTEM_ACCESSTOKEN: $(System.AccessToken)
```

`GRAPHWAKE_KEY` is a secret pipeline variable. Inputs: `spec`, `key`, `url`, `liveBranch`, `failOn` (empty,
`breaking` or `affected`; default empty, never fails), `comment` (default `true`), `agentVersion`. The
`SYSTEM_ACCESSTOKEN: $(System.AccessToken)` mapping gives the agent the build's own token for the comment and the
labels; keep it in the step. Without it the task also tries the token Azure hands every task, but the mapping is the
reliable path. The task fails the step only when the agent exits 1 (a failed check with `failOn` set).

**Permission for the comment.** The build's identity needs to write to pull requests:

1. Project settings > Repositories > select the repository (or "All Repositories") > Security.
2. Select the user `<Project> Build Service (<Organization>)`.
3. Set "Contribute to pull requests" to Allow.

If the pipeline runs with the organization-wide job scope, the identity is `Project Collection Build Service
(<Organization>)`: give it the same permission. Without it the agent prints `could not comment on the pull request
(HTTP 403)` and the build goes on; `failOn` still applies.

Comments and `failOn` work for Azure Repos repositories only. Azure Pipelines building a repository not hosted in
Azure Repos (GitHub, Bitbucket or another Git host) gets no comment and `failOn` is not applied.

Without the extension, run the binary from a script step:

```yaml
  - bash: |
      VERSION=0.2.0
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
      SYSTEM_ACCESSTOKEN: $(System.AccessToken)
```

Azure gives the build's token to scripts only when it is mapped: the `SYSTEM_ACCESSTOKEN: $(System.AccessToken)`
line lets the agent comment and read the labels. The same goes for the Docker image: map the variable in `env` and
pass it with `-e SYSTEM_ACCESSTOKEN`.

To let `--fail-on` fail the build, run the agent outside the `|| echo` and drop `continueOnError`; a failed download
still keeps the step green:

```yaml
  - bash: |
      VERSION=0.2.0
      BASE=https://github.com/graphwake/agent/releases/download/v$VERSION
      ARCHIVE=graphwake_${VERSION}_linux_amd64.tar.gz
      if curl -fsSLO "$BASE/$ARCHIVE" && curl -fsSLO "$BASE/checksums.txt" &&
        sha256sum --check --ignore-missing checksums.txt &&
        tar -xzf "$ARCHIVE" graphwake; then
        ./graphwake upload --spec openapi.json --fail-on affected
      else
        echo "graphwake: agent not run, spec not sent"
      fi
    displayName: Upload spec to Graphwake
    env:
      GRAPHWAKE_KEY: $(GRAPHWAKE_KEY)
      SYSTEM_ACCESSTOKEN: $(System.AccessToken)
```

Azure has no default-branch variable: the live branches are `main` and `master` unless `liveBranch` /
`GRAPHWAKE_LIVE_BRANCH` says otherwise. Pull requests closed without merging are not reported by Azure; their
drafts close on their own after 14 days.

The task is referenced as `GraphwakeUpload@0`, so every `0.x` update of the task reaches every pipeline that uses it.
While the extension is private, changes ship within `0.x` (0.2.0 turned the pull request comment on). Once the
extension is public, a change in default behaviour gets a new major version (`GraphwakeUpload@1`) and `@0` keeps
its behaviour.

### Jenkins

```groovy
stage('API spec') {
  steps {
    withCredentials([string(credentialsId: 'graphwake-key', variable: 'GRAPHWAKE_KEY')]) {
      sh 'docker run --rm -e GRAPHWAKE_KEY -e BRANCH_NAME -e CHANGE_ID -e CHANGE_URL -e CHANGE_AUTHOR -e GIT_COMMIT -e JENKINS_URL -v "$PWD:/w" -w /w ghcr.io/graphwake/agent:0.2.0 upload --spec openapi.json || true'
    }
  }
}
```

Multibranch pipelines give `CHANGE_ID` on pull request builds and `BRANCH_NAME` on branch builds. Or download the
binary with the checksum-verified script from [Install](#install) in an `sh` step (then git is available for the
tip check and the merge message). Pin the image by digest (`ghcr.io/graphwake/agent@sha256:...`) if you want the
same guarantee as the checksum.

No pull request comments on Jenkins yet, and `--fail-on` is not applied there.

### Bitbucket Pipelines

```yaml
definitions:
  steps:
    - step: &graphwake
        name: API spec
        script:
          - ./build-openapi.sh   # produce openapi.json
          - >-
            VERSION=0.2.0; BASE=https://github.com/graphwake/agent/releases/download/v$VERSION;
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

Add `GRAPHWAKE_KEY` as a secured repository variable. No pull request comments on Bitbucket yet, and `--fail-on` is
not applied there.

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
| warning `spec rejected: ...` | The spec is not valid OpenAPI (or AsyncAPI 2.6 / 3.x for an AsyncAPI service), or the service no longer exists. |
| warning `unexpected response 403, spec not sent` | For an AsyncAPI service: the workspace's plan does not include event contracts (Pro and Enterprise). |
| warning `Graphwake unavailable, spec not sent` | Network error or 503 after 3 attempts. |
| warning `spec sent (pull request #42); check failed: breaking changes affect 3 subscribed teams` | `--fail-on` is set and the check failed: exit code 1. |
| `graphwake: spec sent (pull request #42); breaking changes accepted with the label graphwake-accept-breaking` | The label lets the build pass. |
| warning `...; could not comment on the pull request (HTTP 403)` | The comment was not written (permission, fork). Added to the line; the check still applies. |
| warning `...; no GITHUB_TOKEN: cannot comment on the pull request or read its labels` | The token is not passed to the agent: no comment, and the label cannot be read (`--fail-on` still applies). |
| warning `...; --fail-on is not supported on this CI yet; not failing` | No comment and no check on this CI (see [Where it works](#where-it-works)). |

Warnings use `::warning::` on GitHub, `##vso[task.logissue type=warning]` on Azure DevOps and a `WARNING` prefix
elsewhere. The check result and any comment or label warning go on the same line as the result.

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
node --test azure-devops/GraphwakeUpload/index.test.js   # the Azure task (Node 20+)
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

## License

Apache License 2.0. See [LICENSE](LICENSE).
