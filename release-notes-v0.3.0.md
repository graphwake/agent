## Graphwake CI agent v0.3.0

Usage Discovery. The agent has a second command, `graphwake usage`: it reads a gateway log or trace export and sends Graphwake a summary of which caller used which endpoint, how many times and on which day last. It reads one file or standard input, runs wherever you run it (a scheduled CI job or any scheduler) and connects to nothing but Graphwake. Raw paths, query strings and personal-looking caller names stay on that machine.

Optional. Without it, Graphwake still needs only your spec.

No change to how `upload` is used: the same flags, inputs and output as in 0.2.0, with three fixes (see [Fixes](#fixes)).

### Highlights

- **`graphwake usage --source <preset>`** reads `--input <file>` or standard input, fetches the endpoint list of the usage key's usage source from Graphwake, matches every request to an endpoint template on this machine and sends the summary. The key is read from `GRAPHWAKE_USAGE_KEY` (`--key` is accepted); `GRAPHWAKE_KEY`, the spec upload's key, is a different key. Every flag has a variable: `GRAPHWAKE_USAGE_SOURCE`, `GRAPHWAKE_USAGE_INPUT`, `GRAPHWAKE_USAGE_SERVICE`, `GRAPHWAKE_USAGE_CALLER_FIELD`, `GRAPHWAKE_USAGE_NO_COUNTS`, `GRAPHWAKE_USAGE_DRY_RUN`, and `GRAPHWAKE_URL` for the API URL.
- **Seven sources:** `apim` (Azure API Management), `appinsights` (Azure Application Insights, workspace-based, through `az monitor log-analytics query`), `datadog` (Datadog APM, the spans aggregate API), `kong` (Kong file-log or http-log), `nginx` (NGINX, a JSON log format), `otel` (OpenTelemetry Collector file exporter) and `generic` (CSV or JSON from any other tool). `graphwake usage query --source <preset>` prints the query or configuration that produces the input, without a key or a network call.
- **`--dry-run`** fetches the endpoint list, builds the summary and prints the exact body that would be sent, then what stays on the machine: the unmatched requests by host and path prefix and the withheld and anonymous request counts. It sends nothing.
- **`--caller-field`** names the field that identifies the caller when the preset's default does not; **`--service`** names the Graphwake service for requests that no mapping, server URL host or `service` column assigns; **`--no-counts`** sends the summary without request counts.

### Privacy, as the code does it

- **A closed field list.** The summary has the fields `preset`, `agentVersion`, `windowStart`, `windowEnd`, `countsIncluded`, `unmatchedRequests`, `withheldRequests`, `anonymousRequests` and `rows`; each row has `caller`, `callerKind`, `endpointId`, `service`, `method`, `path`, `count` and `lastSeen`. A test fails if a field is added. The row's endpoint, service, method and path template come from the endpoint list Graphwake sent, never from the raw request.
- **Never sent:** raw paths and query strings, hosts and API names (which may be IP addresses), request and response bodies, status codes, withheld caller names (a name that contains an IP address is withheld) and the caller names of unmatched requests. No header value is sent except the one you choose as the caller name (NGINX's suggested format fills `caller` from `$http_x_client_id`; Kong's `--caller-field` can name a header).
- **Withheld caller names:** a name that contains an e-mail address, an IPv4 or IPv6 address, a secret-like string, any run of 32 or more hexadecimal characters, a control character or the sequence `##` is not sent; only the number of its requests is. Names over 200 characters are cut.
- **A caller limit:** with more distinct callers than the limit (2,000 unless Graphwake's endpoint list sets another), nothing is sent and the warning says to choose another `--caller-field`.
- **Counts saturate** at the largest 64-bit integer; `--no-counts` leaves them out.
- **The usage key is never printed.** A dry run only fetches the endpoint list and never sends a summary.
- **Incomplete exports are reported:** a Datadog answer with a status other than `done`, a warning or cut short, or an Application Insights API answer (the `tables` form) with an `error` member that is not null, adds "The export was incomplete (the tool reported a timeout or a warning)." to the result line. Datadog answers carry no dates, so their requests get the day the step runs.

`graphwake usage` always exits 0 and prints one result line (`--dry-run` and `usage query` print several).

### Fixes

- **Larger answers are read whole:** the agent reads up to 16 MB of Graphwake's answer to a spec upload (was 512 KB) and up to 8 MB of a GitHub API answer (was 1 MB), so a large change set or a page of long comments is no longer cut off.
- **The pull request comment is found by a marker at its start** (GitHub and Azure DevOps): a comment that only quotes the hidden marker is no longer taken for the agent's own comment and updated.
- **Azure DevOps:** a token that Azure DevOps does not accept (answered with a sign-in page, HTTP 203) is now reported as an error instead of being taken as a success.

### Install

The GitHub Action, the GitLab template and the Azure DevOps task run `upload` only and now download agent 0.3.0 by default. Run `graphwake usage` with the Docker image or the binary.

**Docker**

```sh
docker run --rm -e GRAPHWAKE_USAGE_KEY -v "$PWD:/w" -w /w ghcr.io/graphwake/agent:0.3.0 usage --source generic --input usage.csv --dry-run
```

**Binary**

```sh
VERSION=0.3.0
BASE=https://github.com/graphwake/agent/releases/download/v$VERSION
ARCHIVE=graphwake_${VERSION}_linux_amd64.tar.gz
curl -fsSLO "$BASE/$ARCHIVE" && curl -fsSLO "$BASE/checksums.txt" &&
  sha256sum --check --ignore-missing checksums.txt &&
  tar -xzf "$ARCHIVE" graphwake &&
  ./graphwake usage --source generic --input usage.csv --dry-run
```

**Spec upload (unchanged)**

```yaml
- uses: graphwake/agent/github-action@v0.3.0
  with:
    spec: openapi.json
    key: ${{ secrets.GRAPHWAKE_KEY }}
```

See the [README](https://github.com/graphwake/agent#usage-discovery-graphwake-usage) for every source, flag, output line and a daily GitHub Actions and Azure Pipelines example. Setup for each tool: https://graphwake.com/guides/find-api-consumers-from-gateway-logs. What the step sends: https://graphwake.com/security/usage-data.

### Assets

| Platform | amd64 | arm64 |
|---|---|---|
| Linux | `graphwake_0.3.0_linux_amd64.tar.gz` | `graphwake_0.3.0_linux_arm64.tar.gz` |
| macOS | `graphwake_0.3.0_darwin_amd64.tar.gz` | `graphwake_0.3.0_darwin_arm64.tar.gz` |
| Windows | `graphwake_0.3.0_windows_amd64.zip` | `graphwake_0.3.0_windows_arm64.zip` |

SHA-256 checksums are in `checksums.txt`. The multi-arch Docker image `ghcr.io/graphwake/agent:0.3.0` (also `:latest`) is published for `linux/amd64` and `linux/arm64`.

### Upgrade notes

- None required. `upload` takes the same flags and inputs as in 0.2.0; pipelines that pin 0.2.0 keep working.
- The GitHub Action (`version` input), the GitLab template (image tag) and the Azure DevOps task (`agentVersion` input, task version 0.3.0, extension version 0.3.0) now default to agent 0.3.0.
- `graphwake usage` needs a usage key from Graphwake. Whatever the server answers, the command prints one line and exits 0.
