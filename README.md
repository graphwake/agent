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
prints exactly one line: what was sent, why nothing was sent, or a warning in your CI's warning format. Two
commands print several lines: `graphwake usage --dry-run` and `graphwake usage query` (and so does `-h`, the help
text). The ingest key, the usage key and the CI token are never printed.

The agent sends the spec as is (YAML or JSON); it does not generate it. See [Producing the spec](#producing-the-spec).

The agent has a second command, `graphwake usage`, for Usage Discovery: it reads a gateway log or trace export and
sends a summary of which caller used which endpoint. See [Usage Discovery](#usage-discovery-graphwake-usage).

Try the check in your browser first: https://graphwake.com/tools/openapi-diff

## Install

| Where | How |
|---|---|
| GitHub Actions | `uses: graphwake/agent/github-action@v0.3.0` ([snippet](#github-actions)) |
| GitLab CI | include `gitlab/graphwake.gitlab-ci.yml` ([snippet](#gitlab-ci)) |
| Azure DevOps | the `GraphwakeUpload` task ([snippet](#azure-devops)) |
| Jenkins, Bitbucket, anything else | the Docker image `ghcr.io/graphwake/agent:0.3.0` or the binary |

Binaries for Linux, macOS and Windows (amd64 and arm64) are on the
[releases page](https://github.com/graphwake/agent/releases), with a `checksums.txt`:

```sh
VERSION=0.3.0
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

`graphwake usage` never returns a non-zero code; see [Output and exit code](#output-and-exit-code).

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
      - uses: graphwake/agent/github-action@v0.3.0
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
  - remote: https://raw.githubusercontent.com/graphwake/agent/v0.3.0/gitlab/graphwake.gitlab-ci.yml

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
      VERSION=0.3.0
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
      VERSION=0.3.0
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
      sh 'docker run --rm -e GRAPHWAKE_KEY -e BRANCH_NAME -e CHANGE_ID -e CHANGE_URL -e CHANGE_AUTHOR -e GIT_COMMIT -e JENKINS_URL -v "$PWD:/w" -w /w ghcr.io/graphwake/agent:0.3.0 upload --spec openapi.json || true'
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
            VERSION=0.3.0; BASE=https://github.com/graphwake/agent/releases/download/v$VERSION;
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

## Usage Discovery (graphwake usage)

`graphwake usage` reads a gateway log or trace export and sends Graphwake a summary: which caller used which
endpoint, how many times and on which day last. It reads one file or standard input, runs wherever you run it (a
scheduled CI job or any scheduler) and connects to nothing but Graphwake. The summary holds the caller name, the
endpoint as a path template, the call count and the last-seen day. Other than the one value you choose as the caller
name, nothing from a request is sent: no body, header, query string or IP address. `--dry-run` prints the summary it
would send, and says so when nothing would be sent.

Optional. Without it, Graphwake still needs only your spec.

The step needs a usage key (`GRAPHWAKE_USAGE_KEY`), which belongs to one usage source in Graphwake. `GRAPHWAKE_KEY`,
the key `upload` uses, is a different key. A run:

1. fetches the endpoint list of the key's usage source: `GET /api/ingest/usage/templates` returns the services,
   their server URLs and endpoint templates, the mappings set in Graphwake and the limits;
2. reads the input with the preset and matches every request to an endpoint template, on this machine;
3. sends the summary: `POST /api/ingest/usage`. With `--dry-run` it prints the summary instead: it sends no
   summary; it only fetches the endpoint list with the usage key (step 1).

A request finds its endpoint in this order: a mapping set in Graphwake (host, path prefix, API name; the one with
the most of them wins), else the host of a service's server URL, else the service the input names (`generic`) or
`--service`. The method and the path, segment by segment, then pick the endpoint template. A request that matches
no endpoint adds no row.

**Known limits.** A template with a parameter inside a segment, such as `/files/{id}.json`, `/orders/{id}:cancel`
or `/v{version}/x`, is not matched yet: requests to it count as matching no endpoint.

### Supported sources

| Source | Tool | Also accepted | Input | Caller name (kind) | `--caller-field` names |
|---|---|---|---|---|---|
| `apim` | Azure API Management | `azure-apim`, `api-management` | The JSON that `az monitor log-analytics query -o json` writes for the printed query over `ApiManagementGatewayLogs` (requests of the last 7 days, grouped). | The subscription, `ApimSubscriptionId` (`client`). | A column of the result. |
| `appinsights` | Azure Application Insights | `app-insights`, `applicationinsights`, `application-insights` | The JSON that `az monitor log-analytics query -o json` writes for the printed query over `AppDependencies` (outgoing HTTP calls of the last 7 days, grouped; workspace-based Application Insights). The answer of `az monitor app-insights query` is read too when its columns are named as in the printed query (`Host`, `Method`, `Path`, `Calls`, `LastSeen`), with the caller in `AppRoleName` or the classic `cloud_RoleName`. | The calling service, `AppRoleName`, else `cloud_RoleName` (`service`). | A column of the result. |
| `datadog` | Datadog APM | `dd` | The answers of the spans aggregate API (`POST /api/v2/spans/analytics/aggregate`) that the printed `curl` call appends to one file, one call per calling service. | The calling service, the `service` facet (`service`). | A facet of the bucket's `by`. |
| `generic` | Any other tool | `csv`, `json` | CSV with a header line, a JSON array of objects or one JSON object per line, with the columns `caller,callerKind,service,method,path,count,lastSeen`. `caller` (or the `--caller-field` column), `method`, `path` and `lastSeen` are required; `callerKind`, `service` and `count` may be left out or empty. | The `caller` column (`callerKind`: `client` or `service`, default `client`). | A column. |
| `kong` | Kong | | The file-log plugin's log (one JSON object per line) or an http-log batch (a JSON array). | The consumer: `consumer.username`, else `custom_id`, else `id` (`client`). | A dotted path, such as `consumer.custom_id` or `request.headers.x-client-id`. |
| `nginx` | NGINX | | An access log written in the JSON log format the printed configuration adds (one JSON object per line). | The `caller` member, which that format fills from the `X-Client-Id` request header (`client`). | A member of the log line. |
| `otel` | OpenTelemetry | `opentelemetry`, `otlp` | The OpenTelemetry Collector file exporter's OTLP JSON (one object per line), with the printed Collector configuration, which keeps the HTTP client spans. Its `filter` processor's `trace_conditions` needs the Collector's contrib distribution, 0.146.0 or later. | The calling service, the resource attribute `service.name` (`service`). | A resource attribute. |

"The printed query" is what `graphwake usage query --source <preset>` prints: the query, the API call or the
configuration that produces the input (for `generic`, the header line and one example line). It needs no key and
makes no network call. Source names ignore letter case.

- `apim`, `appinsights`, `datadog` and `generic` read request counts from the input: `Calls` in `apim` and
  `appinsights`, the `c0` count in `datadog`, `count` in `generic` (empty: one request). In `kong`, `nginx` and
  `otel` every line or span is one request.
- `datadog`: the answers carry no dates, so every request gets the day the step runs (UTC).
- `kong` reads only `consumer`, `request.method`, `request.uri`, `request.headers.host` (or the host in
  `request.url`), `started_at` and the `--caller-field` path of an entry; every other member is passed over
  without being kept.
- `otel` reads only client spans with an HTTP method, a path and a start time; other spans are not counted.
- In `generic`, `service` is the Graphwake service name and `path` is a concrete path or a template such as
  `/orders/{id}`.
- In every preset, the query string of the request path is dropped before the path is matched.
- A line or element a preset cannot read is skipped and counted; its content is never printed. In an input read
  line by line (anything but a JSON array or a Datadog or Application Insights answer), a line longer than 16 MB
  counts as one: it is read past without being kept.

### Flags and environment

A flag wins over the variable. The two switches take `true`, `1`, `yes` or `on` in the variable.

| Flag | Variable | Meaning |
|---|---|---|
| `--source` | `GRAPHWAKE_USAGE_SOURCE` | The preset or one of its other names (see [Supported sources](#supported-sources)). Required. |
| `--input` | `GRAPHWAKE_USAGE_INPUT` | The file to read. Default: standard input, when something is piped into it (a terminal is not read). In CI always pass `--input`: the agent waits for piped input until the pipe is closed. |
| `--key` | `GRAPHWAKE_USAGE_KEY` | The usage key. Required, also for `--dry-run`. Use the variable from a CI secret; the flag is accepted but shows up in process lists. |
| `--url` | `GRAPHWAKE_URL` | Graphwake API URL, the same variable `upload` reads. Default `https://api.graphwake.com`. |
| `--service` | `GRAPHWAKE_USAGE_SERVICE` | The Graphwake service a request belongs to when no mapping, no server URL host and no `service` column decides it. It must name a service in the endpoint list. Default: none. |
| `--caller-field` | `GRAPHWAKE_USAGE_CALLER_FIELD` | The field that names the caller (see the last column of [Supported sources](#supported-sources)). Default: the preset's. |
| `--no-counts` | `GRAPHWAKE_USAGE_NO_COUNTS=true` | Send no per-row request counts: the rows have no `count` and `countsIncluded` is `false`. The three totals (`unmatchedRequests`, `withheldRequests`, `anonymousRequests`) are still sent. Default: off, counts are sent. |
| `--dry-run` | `GRAPHWAKE_USAGE_DRY_RUN=true` | Fetch the endpoint list, build the summary and print it with what is not sent (see [What the usage step sends](#what-the-usage-step-sends)), and say so when the summary has no rows and would not be sent. It sends no summary; it only fetches the endpoint list with the usage key. Default: off. |

`graphwake usage -h` prints the help.

### What the usage step sends

The agent makes two requests, both to the Graphwake API URL: `GET /api/ingest/usage/templates` and
`POST /api/ingest/usage`. With `--dry-run` it makes only the first. The agent sets the headers
`Authorization: Bearer <usage key>`, `Accept: application/json` and
`User-Agent: graphwake-agent/<version> (<CI platform>)`, and `Content-Type: application/json` on the POST. A request
is tried up to 3 times on a network error or HTTP 503, for at most 30 seconds in all. A redirect is never followed:
a 3xx answer ends the run with `unexpected response 3xx`, and nothing goes to the address it names.

The POST body is the summary, and the summary has these fields and no others:

| Field | What it holds |
|---|---|
| `preset` | The preset's name, such as `apim`. |
| `agentVersion` | The agent's version. |
| `windowStart` | The earliest day (UTC, `YYYY-MM-DD`) of the requests read. |
| `windowEnd` | The latest day of the requests read. |
| `countsIncluded` | `false` with `--no-counts`. |
| `unmatchedRequests` | How many requests matched no endpoint (of those with a caller name that is not withheld). A number only. |
| `withheldRequests` | How many requests had a withheld caller name. A number only. |
| `anonymousRequests` | How many requests had no caller name. A number only. |
| `rows` | One row per caller, caller kind and endpoint, with the fields below. |

| Field | What it holds |
|---|---|
| `caller` | The caller name: the value of the caller field without white space at its ends, cut to its first 200 characters (a certificate subject: its common name). Never a name the rules below withhold. |
| `callerKind` | `client` (a client of the gateway) or `service` (a service that calls another). |
| `endpointId` | The endpoint's id, from the endpoint list Graphwake sent. |
| `service` | The service's name, from that list. |
| `method` | The endpoint's method, from that list. |
| `path` | The endpoint's path template from that list, every parameter written `{}`, such as `/orders/{}`. Never the raw path. |
| `count` | The number of requests. Left out with `--no-counts`. It stops at the largest 64-bit integer instead of overflowing. |
| `lastSeen` | The latest day (UTC) the caller used the endpoint. |

Each row's endpoint id, service, method and path template come from the endpoint list Graphwake sent. From the
input, a row gets only these: the value of the field you choose as the caller (the preset's default or
`--caller-field`), sent as the caller name unless it is withheld; in `generic`, the `callerKind` column (`client` or
`service`; the other presets set the kind themselves); the number of requests; and their days. Nothing is
sent from any other field: not a request's path or query string, host, API name, other headers, body, status code
or client IP address. Those only find the endpoint, on this machine, or are not read at all; `--dry-run` also
prints the host and a masked path prefix of the requests that matched no endpoint (see below).

Headers are the common case of that rule: no header value is sent except the one you choose as the caller name. The
NGINX format that `graphwake usage query --source nginx` prints fills `caller` from the `X-Client-Id` header
(`$http_x_client_id`), and Kong's `--caller-field` can name a header such as `request.headers.x-client-id`.

The caller name is whatever that field holds, so point the caller field at a field that names the application (a
subscription, a consumer, a service). The agent does this with the value, in this order:

1. White space at both ends is removed. An empty value is no caller name.
2. A certificate subject is reduced to its common name; only the common name is sent, and it goes through the
   next steps like any other name. From `CN=checkout,OU=Payments,O=Acme` the caller name is `checkout`. This is
   for mutual TLS (NGINX's `$ssl_client_s_dn`). A value is read as a subject only when all of it is one: parts of
   the form `TYPE=value`, separated by `,` or `+`, or each led by `/` in the older form (`/O=Acme/CN=checkout`),
   every type one of `CN`, `O`, `OU`, `C`, `L`, `ST`, `STREET` or `SERIALNUMBER` (in any letter case; spaces
   around a type or a value are ignored), with exactly one `CN` that is not empty. Any other value stays as it is.
   That includes a subject the agent does not read: one without a common name or with two, with another type, or
   with a value it cannot read with confidence (a quoted value or a hexadecimal escape, for example). A subject
   with a `UID`, `DC`, `emailAddress` or `E` part is not read either, because it may be a person's (a user's
   directory entry or a personal certificate, whose common name is the person's name): from
   `CN=Jane Doe,emailAddress=jane@contoso.com` or `cn=John Smith,ou=people,dc=example,dc=com` no common name is
   taken, and a service certificate with such a part is withheld whole too. Such a subject still has its `=`, so
   the rules below withhold it.
3. A value that matches one of the rules below is withheld.
4. Any other value becomes the caller name, whatever it is (a host name or a status code, if the caller field
   names one). A value over 200 characters is cut to its first 200, and white space at the end of the cut is
   removed. The cut value goes through the rules below again: a cut can make a name they withhold (from
   `... 10.0.0.1299` it can leave `... 10.0.0.1`), and such a name is withheld too.

A request that matched no endpoint adds no row, so a caller seen only in such requests is not named. Not sent
either: the table of unmatched requests by host and path prefix, and the content of lines that could not be read.

`--dry-run` prints that table to its output, which in CI is the job log, so the table is kept short and masked:
each row starts with `- `, so no host or path from the input can begin a line as a CI command; it shows the first
two segments of the path, and a segment with a digit, `@` or `%`, or longer than 24 characters, is printed as `*`
(`/users/jane@contoso.com` as `/users/*`, `/v1/orders/8812` as `/*/orders`); it lists the 20 host and prefix pairs
with the most requests, then `- and N more`. The agent keeps at most 2,000 pairs; the requests of any further pair
are counted in one `- (other)` row.

**Withheld caller names.** A caller name is withheld when any of these applies, to the name as it stands after
steps 1 and 2, or to the name as it is cut in step 4; its requests add no row and are counted in `withheldRequests`. Letters and
digits here are the ASCII ones (`a` to `z`, `A` to `Z`, `0` to `9`).

- It contains an e-mail address of this form: one or more letters, digits or `._%+-`, then `@`, then groups of
  letters, digits and hyphens joined by single dots, where a group after the first begins with two or more letters:
  `jane@contoso.com`. Not caught: a domain without such a dot, as in `jane@localhost`, and an address with another
  character right before the `@`, as in `jané@contoso.com`.
- It contains an IPv4 address: four numbers from 0 to 255, without a leading zero, joined by dots, with no
  letter or digit directly before the first number or directly after the last: `10.0.0.12`, `10.0.0.12:8080`,
  `10.0.0.12.nip.io`, `src:10.0.0.12`. Any four such numbers count, a version such as `app 1.2.3.4` too. Not
  caught: `ip10.0.0.12` (a letter before the first number), `010.0.0.12` (a leading zero), `ip-10-0-0-12` (no dots).
- It contains an IPv6 address in its usual written form, with at least one hexadecimal digit, as a whole piece.
  The pieces are what is left when the name is split at every character other than a letter, a digit, `.` or `:`,
  without the dots at a piece's ends: `2001:db8::1`, `[fe80::1]:443`, `client 2001:db8::1`. Not caught: a
  piece that holds more than the address, as in `src:2001:db8::1` and `2001:db8::1.nip.io`.
- It contains a run of 32 or more letters, digits and `+/_=-` that, at its full length, has an upper-case letter,
  a lower-case letter and a digit: `dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk`. A run that is exactly a UUID in
  its usual form, such as `550e8400-e29b-41d4-a716-446655440000`, is not withheld. Not caught: a long run without
  one of the three, such as `tok_abcdefghijklmnopqrstuvwxyz0123456789ab`.
- It contains 32 or more hexadecimal characters (`0` to `9`, `a` to `f`, `A` to `F`) in a row, also behind a
  label: `token-da39a3ee5e6b4b0d3255bfef95601890afd80709`.
- It contains a control character, such as a newline, a carriage return, a tab or ESC.
- It contains the sequence `##`, as in `x ##y`.
- It looks like a path or a URL: it starts with `/`, or it contains `://` or `?`, or one of its words (separated
  by white space) starts with `/` and is longer than one character: `/v1/orders/8812`, `https://orders.example/a`,
  `orders?page`, `GET /v1/orders/8812`. Not caught: a path without the leading `/` or behind a label, as in
  `v1/orders/8812` and `uri:/v1/x`. `team/app` and `Checkout / Web` are not withheld either.
- It contains any of `{`, `}`, `"`, `<`, `>` or `=`, the characters of a JSON or XML body or of a `key=value` query
  string: `{"card":"4111"}`, `page=2&token=abc`, `<order id="1">`. Not caught: text without one of these six
  characters, such as `card:4111,cvc:123`; `&` is not one of them, so `R&D team` is not withheld.

**A safety net, not a guarantee.** These rules catch common shapes of personal or secret values. A value in another
shape is not withheld: `Jane Doe` and `hunter2` are not. So choose a caller field that holds a client or application
name, and look at the caller names a `--dry-run` prints before the first real run.

**Limits.** With more distinct callers (caller name and kind pairs) than the caller limit (2,000, unless the
endpoint list Graphwake sends sets another), nothing is sent: so many names usually mean the caller field names end
users, and the warning says to choose another with `--caller-field`. With more rows than the row limit (50,000
unless the list sets another) or a summary over 10 MB, nothing is sent either. The agent stops keeping new callers
and rows as soon as a limit is passed, so their number cannot exhaust its memory; that is why the warning says
"more than" the limit instead of a count.

**Example.** With an endpoint list that has one service, `orders` (server URL `https://orders.internal/v1`;
endpoints `GET /orders/{}`, `POST /orders` and `GET /orders`), and this input:

```csv
caller,callerKind,service,method,path,count,lastSeen
checkout-web,client,orders,GET,/v1/orders/8812?token=abc,42,2026-11-17
billing-worker,Service,orders,post,/v1/orders,,2026-11-18T09:12:44.1234567Z
"mobile app, ios",client,,GET,/orders/{},3,2026-11-16
,client,orders,GET,/v1/orders/77,5,2026-11-15
```

`graphwake usage --source generic --input usage.csv --dry-run` (agent 0.3.0) prints:

```text
--- would send ---
{"preset":"generic","agentVersion":"0.3.0","windowStart":"2026-11-15","windowEnd":"2026-11-18","countsIncluded":true,"unmatchedRequests":3,"withheldRequests":0,"anonymousRequests":5,"rows":[{"caller":"billing-worker","callerKind":"service","endpointId":"ep-create","service":"orders","method":"POST","path":"/orders","count":1,"lastSeen":"2026-11-18"},{"caller":"checkout-web","callerKind":"client","endpointId":"ep-get","service":"orders","method":"GET","path":"/orders/{}","count":42,"lastSeen":"2026-11-17"}]}
--- end ---
Not sent to Graphwake:
  Requests that matched no endpoint, by host and path prefix:
    - (no host) /orders/{}  3
  Requests withheld because the caller name looked personal: 0
  Requests without a caller name: 5
```

The line between the markers is the exact body that would be sent. `/v1/orders/8812?token=abc` became the template
`/orders/{}`; the `mobile app, ios` row names no service and matched nothing, so it adds no row and its 3 requests
are in `unmatchedRequests`; the row without a caller adds its 5 requests to `anonymousRequests`. Their days still
count for `windowStart` and `windowEnd`.

### Output and exit code

`graphwake usage` prints one line, except `--dry-run` (the example above) and `graphwake usage query` (the query or
configuration). The usage key is never printed. Warnings use the CI's warning format, as in [Output](#output).

| Line | Meaning |
|---|---|
| `graphwake: usage sent: 2 callers, 3 endpoint rows (1 new caller, 2 suggestions, 0 subscriptions added).` | Sent. The numbers are Graphwake's answer. |
| `graphwake: usage sent.` | Sent; Graphwake's answer could not be read as the expected JSON. |
| `... 5 requests matched no endpoint.` | Added to the sent line: counted, not sent by name. |
| `... 7 requests had no caller name.` | Added to the sent line. |
| `... 3 requests were withheld because the caller name looked personal.` | Added to the sent line. |
| `... 1 line could not be read.` | Added to the sent line: lines or elements the preset skipped. |
| `... 4 rows were not accepted.` | Added to the sent line: rows Graphwake did not take. |
| `... The export was incomplete (the tool reported a timeout or a warning).` | Added to the sent line, last. What the export held was sent. |
| `Note: the export was incomplete (the tool reported a timeout or a warning).` | Printed after a `--dry-run` summary when the export was incomplete. |
| `Note: this summary has no rows, so it would not be sent.` | The last line of a `--dry-run` whose summary has no rows: the same run without `--dry-run` sends nothing and warns why. |
| warning `usage not sent: no source given (one of: apim, appinsights, datadog, generic, kong, nginx, otel)` | No `--source`. An unknown name gives `unknown source "<name>" (one of: ...)`; the name is repeated only when it is up to 20 lower-case letters, digits and hyphens, else the line is `unknown source (one of: ...)`. |
| warning `usage not sent: invalid arguments: ...` | An unknown flag, a flag without its value or an invalid value (such as `--no-counts=maybe`). `graphwake usage query` prints the same line. |
| warning `usage not sent: unexpected argument (flags take the form --name value)` | A word that is not a flag. |
| warning `usage not sent: no usage key (set GRAPHWAKE_USAGE_KEY)` | No key. |
| warning `usage not sent: GRAPHWAKE_URL must be an http(s) URL` | The API URL is not `http` or `https`. |
| warning `usage not sent: the input file could not be opened` | `--input` names a file that cannot be read. |
| warning `usage not sent: the input file is empty` | The file holds nothing but white space. |
| warning `usage not sent: no input (give --input or pipe a file)` | No `--input` and nothing piped in. |
| warning `usage not sent: the input could not be read` | Not one line the preset can read (for `generic` CSV also: no header line with the required columns), or a read error. |
| warning `usage not sent: the input held no requests` | The input was read but held no request. |
| warning `usage not sent: the export was incomplete (the tool reported a timeout or a warning) and held no requests` | An incomplete export without a single request. |
| warning `usage not sent: --service names no service in the endpoint list Graphwake sent` | Check `--service`. |
| warning `usage not sent: too many distinct callers (more than 2000, limit 2000). The caller field may identify end users; choose another with --caller-field` | Over the caller limit. Graphwake's own check gives the same sentence without the numbers. |
| warning `usage not sent: the summary is too large` | Over the row limit or 10 MB (the agent's check or Graphwake's). |
| warning `usage not sent: the summary could not be built` | An internal problem building the summary. |
| warning `usage not sent: no caller names were found in the input. Check --caller-field` | No request had a caller name. |
| warning `usage not sent: no caller name could be sent: every name was withheld (it matched a withholding rule: see the README) or was missing. Check --caller-field` | Every request was withheld or had no caller name. |
| warning `usage not sent: nothing in the input matched an endpoint` | Check the mappings, the server URLs or `--service`; `--dry-run` shows the unmatched hosts and paths. |
| warning `usage not sent: Graphwake's endpoint list could not be read` | The endpoint list answer was not valid. |
| warning `usage not sent: the key was not accepted` | HTTP 401: check `GRAPHWAKE_USAGE_KEY`. |
| warning `usage not sent: the Graphwake account is closed` | HTTP 402. |
| warning `usage not sent: Usage Discovery is not in your plan` | HTTP 403. |
| warning `usage not sent: Graphwake rejected the summary as invalid` | HTTP 400. |
| warning `usage not sent: Graphwake is rate limiting this key; try again later` | HTTP 429. |
| warning `usage not sent: Graphwake could not be reached` | Network error or HTTP 503 after 3 attempts. |
| warning `usage not sent: unexpected response 500` | Any other status, a redirect (3xx) too: redirects are never followed. |
| warning `unknown source "<name>" (one of: ...)` | `graphwake usage query` with an unknown or missing source (`no source given (one of: ...)`). |
| warning `usage not sent: internal error` | A bug in the agent; it still exits 0. Always `WARNING graphwake: usage not sent: internal error`, whatever the CI. |

"The export was incomplete" means: a Datadog answer whose `meta` reports a status other than `done` or a warning,
or an answer cut short (it ends early, or the next answer starts where its next bucket belongs); or an Application
Insights API answer (the `tables` form) with an `error` member that is not null.

The exit code is always 0: `graphwake usage` never fails the pipeline, whatever it prints.

### Running it on a schedule

Run the step where your logs are, once a day, after the step that writes the export. The examples use the `generic`
preset and the Docker image; the arguments after the image name replace its default command (`upload`). Add
`GRAPHWAKE_USAGE_KEY` as a secret. The GitHub Action, the GitLab template and the Azure DevOps task only upload
specs.

GitHub Actions:

```yaml
on:
  schedule:
    - cron: "0 5 * * *"   # daily, 05:00 UTC
  workflow_dispatch:

jobs:
  api-usage:
    runs-on: ubuntu-latest
    steps:
      # ... write the requests of the last days to usage.csv (see "graphwake usage query --source generic")
      - name: Send usage to Graphwake
        env:
          GRAPHWAKE_USAGE_KEY: ${{ secrets.GRAPHWAKE_USAGE_KEY }}
        run: >-
          docker run --rm -e GRAPHWAKE_USAGE_KEY -v "$PWD:/w" -w /w
          ghcr.io/graphwake/agent:0.3.0 usage --source generic --input usage.csv
          || echo "graphwake: agent not run, usage not sent"
```

Azure Pipelines:

```yaml
schedules:
  - cron: "0 5 * * *"   # daily, 05:00 UTC
    displayName: Daily usage summary
    branches:
      include: [main]
    always: true

trigger: none
pr: none

pool:
  vmImage: ubuntu-latest

steps:
  # ... write the requests of the last days to usage.csv (see "graphwake usage query --source generic")
  - bash: |
      docker run --rm -e GRAPHWAKE_USAGE_KEY -v "$PWD:/w" -w /w \
        ghcr.io/graphwake/agent:0.3.0 usage --source generic --input usage.csv ||
        echo "graphwake: agent not run, usage not sent"
    displayName: Send usage to Graphwake
    env:
      GRAPHWAKE_USAGE_KEY: $(GRAPHWAKE_USAGE_KEY)
```

Inside the container the agent sees only the variables passed with `-e`, so its warnings start with
`WARNING graphwake:`. Piping the export works too (give `docker run` the `-i` flag), but in CI prefer `--input`:
piped input is read until the pipe is closed.

Setup for each tool (the export step, the query, the schedule):
https://graphwake.com/guides/find-api-consumers-from-gateway-logs. What the step sends, for security reviews:
https://graphwake.com/security/usage-data.

## Building and releasing

```sh
go vet ./... && go test ./...
node --test azure-devops/GraphwakeUpload/index.test.js   # the Azure task (Node 20+)
go build -o graphwake ./cmd/graphwake
```

Standard library only; Go 1.27.3 or later (the `go` line of `go.mod`, which the release workflow builds with).
Releases are made with GoReleaser (`.goreleaser.yaml`): binaries, `checksums.txt` and a
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
