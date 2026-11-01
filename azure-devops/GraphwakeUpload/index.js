// Azure DevOps task: download the graphwake agent for this agent's OS/arch, verify its checksum and run
// `graphwake upload`. Node built-ins only. It never fails the pipeline: problems are warnings, exit code 0.
"use strict";

const crypto = require("crypto");
const fs = require("fs");
const http = require("http");
const https = require("https");
const os = require("os");
const path = require("path");
const { spawnSync } = require("child_process");

const RELEASES = "https://github.com/graphwake/agent/releases/download";
// Download, checksum and upload together; the agent itself caps its own work well below this.
const OVERALL_TIMEOUT_MS = 180 * 1000;
// The agent's worst case is about 2 minutes (spec URL ~60 s, ingest 30 s, git checks).
const AGENT_TIMEOUT_MS = 150 * 1000;

function input(name) {
  return (process.env["INPUT_" + name.toUpperCase()] || "").trim();
}

function warn(message) {
  console.log("##vso[task.logissue type=warning]graphwake: " + message.replace(/%/g, "%AZP25"));
}

// download fetches url into file. It rejects on HTTP errors, a stalled socket (60 s idle), a connection dropped
// mid-body and non-https redirects; it never hangs.
function download(url, file, redirects = 5) {
  return new Promise((resolve, reject) => {
    let settled = false;
    let out = null;
    const fail = (err) => {
      if (settled) return;
      settled = true;
      if (out) out.destroy();
      req.destroy();
      reject(err);
    };
    const client = url.startsWith("https:") ? https : http;
    const req = client.get(url, { headers: { "User-Agent": "graphwake-azure-task" }, timeout: 60000 }, (res) => {
      res.on("error", fail);
      res.on("aborted", () => fail(new Error("connection dropped")));
      res.on("close", () => {
        if (!res.complete) fail(new Error("connection dropped"));
      });
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location && redirects > 0) {
        const next = new URL(res.headers.location, url);
        res.resume();
        if (url.startsWith("https:") && next.protocol !== "https:") {
          fail(new Error("insecure redirect"));
          return;
        }
        settled = true;
        resolve(download(next.toString(), file, redirects - 1));
        return;
      }
      if (res.statusCode !== 200) {
        res.resume();
        fail(new Error("HTTP " + res.statusCode));
        return;
      }
      out = fs.createWriteStream(file);
      out.on("error", fail);
      out.on("finish", () => {
        if (settled) return;
        if (!res.complete) {
          fail(new Error("connection dropped"));
          return;
        }
        settled = true;
        out.close(() => resolve());
      });
      res.pipe(out);
    });
    req.on("timeout", () => fail(new Error("timeout")));
    req.on("error", fail);
  });
}

function sha256(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

async function main() {
  const goos = { linux: "linux", darwin: "darwin", win32: "windows" }[process.platform];
  const goarch = { x64: "amd64", arm64: "arm64" }[process.arch];
  if (!goos || !goarch) {
    warn("unsupported agent platform " + process.platform + "/" + process.arch + ", spec not sent");
    return;
  }
  const version = (input("agentVersion") || "0.1.0").replace(/^v/, "");
  const ext = goos === "windows" ? "zip" : "tar.gz";
  const bin = goos === "windows" ? "graphwake.exe" : "graphwake";
  const archive = `graphwake_${version}_${goos}_${goarch}.${ext}`;
  const dir = path.join(process.env.AGENT_TEMPDIRECTORY || os.tmpdir(), "graphwake-" + version);
  const binPath = path.join(dir, bin);

  if (!fs.existsSync(binPath)) {
    fs.mkdirSync(dir, { recursive: true });
    const base = `${RELEASES}/v${version}`;
    try {
      await download(`${base}/${archive}`, path.join(dir, archive));
      await download(`${base}/checksums.txt`, path.join(dir, "checksums.txt"));
    } catch (e) {
      warn("could not download the agent " + version + " (" + e.message + "), spec not sent");
      return;
    }
    const expected = fs
      .readFileSync(path.join(dir, "checksums.txt"), "utf8")
      .split(/\r?\n/)
      .map((line) => line.trim().split(/\s+/))
      .find((parts) => parts[1] === archive);
    if (!expected || expected[0] !== sha256(path.join(dir, archive))) {
      warn("agent checksum mismatch, spec not sent");
      return;
    }
    // tar reads zip archives too (bsdtar ships with Windows 10+).
    const unpack = spawnSync("tar", ["-xf", path.join(dir, archive), "-C", dir, bin], { stdio: "ignore", timeout: 60000 });
    if (unpack.status !== 0 || !fs.existsSync(binPath)) {
      warn("could not unpack the agent, spec not sent");
      return;
    }
  }

  const env = Object.assign({}, process.env, {
    GRAPHWAKE_SPEC: input("spec"),
    GRAPHWAKE_KEY: input("key"),
    GRAPHWAKE_URL: input("url"),
    GRAPHWAKE_LIVE_BRANCH: input("liveBranch"),
  });
  delete env.INPUT_KEY;
  // spawnSync blocks the event loop, so the overall timer cannot fire meanwhile: the child gets its own cap.
  const run = spawnSync(binPath, ["upload"], { stdio: "inherit", env, timeout: AGENT_TIMEOUT_MS });
  if (run.error) warn("the agent did not finish (" + run.error.message + "), spec may not have been sent");
}

// Never fail the pipeline: whatever happens, warn and exit 0.
function bail(message) {
  warn(message + ", spec not sent");
  process.exit(0);
}

if (require.main === module) {
  process.on("uncaughtException", (e) => bail("unexpected error (" + (e && e.message) + ")"));
  process.on("unhandledRejection", (e) => bail("unexpected error (" + (e && e.message) + ")"));
  setTimeout(() => bail("the task took longer than " + OVERALL_TIMEOUT_MS / 1000 + " s"), OVERALL_TIMEOUT_MS);
  main()
    .catch((e) => warn("unexpected error (" + (e && e.message) + "), spec not sent"))
    .finally(() => process.exit(0));
}

module.exports = { download };
