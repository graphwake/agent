## Graphwake CI agent v0.3.0

Usage Discovery. The agent has a second command, `graphwake usage`: it reads a gateway log or trace export and sends Graphwake a summary of which caller used which endpoint, how many times and on which day last. It reads one file or standard input, runs wherever you run it (a scheduled CI job or any scheduler) and connects to nothing but Graphwake. The summary holds the caller name, the endpoint as a path template, the call count and the last-seen day. Other than the one value you choose as the caller name, nothing from a request is sent: no body, header, query string or IP address.

Optional. Without it, Graphwake still needs only your spec.

`upload` is unchanged in this release. The top-level help (`graphwake -h`) now also lists the usage commands.

### Highlights

- **`graphwake usage --source <preset>`** reads `--input <file>` or standard input, fetches the endpoint list of the usage key's usage source from Graphwake, matches every request to an endpoint template on this machine and sends the summary. The key is read from `GRAPHWAKE_USAGE_KEY` (`--key` is accepted); `GRAPHWAKE_KEY`, the spec upload's key, is a different key. The other variables: `GRAPHWAKE_USAGE_SOURCE`, `GRAPHWAKE_USAGE_INPUT`, `GRAPHWAKE_USAGE_SERVICE`, `GRAPHWAKE_USAGE_CALLER_FIELD`, `GRAPHWAKE_USAGE_NO_COUNTS`, `GRAPHWAKE_USAGE_DRY_RUN`, and `GRAPHWAKE_URL` for the API URL (`--url`).
- **Seven sources:** `apim` (Azure API Management), `appinsights` (Azure Application Insights, workspace-based, through `az monitor log-analytics query`), `datadog` (Datadog APM, the spans aggregate API), `kong` (Kong file-log or http-log), `nginx` (NGINX, a JSON log format), `otel` (OpenTelemetry Collector file exporter) and `generic` (CSV or JSON from any other tool). `graphwake usage query --source <preset>` prints the query or configuration that produces the input, without a key or a network call.
- **`--dry-run`** fetches the endpoint list, builds the summary and prints the exact body that would be sent, then what stays on the machine: the unmatched requests by host and path prefix and the withheld and anonymous request counts. When the summary has no rows, it ends with `Note: this summary has no rows, so it would not be sent.`, because the real run sends nothing then. It sends nothing.
- **`--caller-field`** names the field that identifies the caller when the preset's default does not; **`--service`** names the Graphwake service for requests that no mapping, server URL host or `service` column assigns; **`--no-counts`** sends the summary without request counts.

### Privacy, as the code does it

- **A closed field list.** The summary has the fields `preset`, `agentVersion`, `windowStart`, `windowEnd`, `countsIncluded`, `unmatchedRequests`, `withheldRequests`, `anonymousRequests` and `rows`; each row has `caller`, `callerKind`, `endpointId`, `service`, `method`, `path`, `count` and `lastSeen`. A test fails if a field is added. The row's endpoint, service, method and path template come from the endpoint list Graphwake sent, never from the raw request.
- **From the input, only the caller field is sent as free text.** The value of the field you choose as the caller (the preset's default or `--caller-field`) is sent as the caller name unless it is withheld. Besides the request counts, the days and, in the `generic` preset, the `client` or `service` kind from the `callerKind` column, nothing is sent from any field other than the chosen caller field: not a request's path or query string, host, API name, body, status code or client IP address. No header value is sent except the one you choose as the caller name (NGINX's suggested format fills `caller` from `$http_x_client_id`; Kong's `--caller-field` can name a header). The caller name is whatever that field holds: a value that no rule below withholds, such as a host name or a status code, is sent if the caller field names one, so point it at a field that names the application.
- **Withheld caller names:** a name that contains an e-mail address, an IPv4 or IPv6 address in its standard written form, a secret-like string, any run of 32 or more hexadecimal characters, a control character or the sequence `##`; that looks like a path or a URL (without surrounding spaces it starts with `/`, or it contains `://` or `?`, or one of its words starts with `/` and is longer than one character, as in `GET /v1/orders/8812`); or that contains any of `{`, `}`, `"`, `<`, `>` or `=` (a JSON or XML body, a `key=value` query string) is not sent; only the number of its requests is. Other ways of writing an IP address, such as `010.0.0.12`, are not caught. Names over 200 characters are cut.
- **A caller limit:** with more distinct callers (caller name and kind pairs) than the limit (2,000 unless Graphwake's endpoint list sets another), nothing is sent and the warning says to choose another `--caller-field`.
- **Counts saturate** at the largest 64-bit integer; `--no-counts` leaves them out.
- **The usage key is never printed.** A dry run only fetches the endpoint list and never sends a summary.
- **Incomplete exports are reported:** a Datadog answer with a status other than `done`, a warning or cut short, or an Application Insights API answer (the `tables` form) with an `error` member that is not null, adds "The export was incomplete (the tool reported a timeout or a warning)." to the result line. Datadog answers carry no dates, so their requests get the day the step runs.

`graphwake usage` always exits 0 and prints one result line (`--dry-run` and `usage query` print several).

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

- None required. `upload` is unchanged; pipelines that pin 0.2.0 keep working.
- The GitHub Action (`version` input), the GitLab template (image tag) and the Azure DevOps task (`agentVersion` input, task version 0.3.0, extension version 0.3.0) now default to agent 0.3.0.
- `graphwake usage` needs a usage key from Graphwake. Whatever the server answers, the command exits 0.
