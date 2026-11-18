// Run with: node --test azure-devops/GraphwakeUpload/index.test.js   (Node 20+). Checks that download() never hangs,
// which exit code the task passes on and where the build token comes from.
"use strict";

const assert = require("node:assert");
const fs = require("node:fs");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { accessToken, download, exitCodeFor } = require("./index.js");

function serve(handler) {
  return new Promise((resolve) => {
    const srv = http.createServer(handler);
    srv.listen(0, "127.0.0.1", () => resolve(srv));
  });
}

const tmp = () => path.join(fs.mkdtempSync(path.join(os.tmpdir(), "gw-")), "file");

test("downloads a file", async () => {
  const srv = await serve((req, res) => res.end("hello"));
  const file = tmp();
  await download(`http://127.0.0.1:${srv.address().port}/a`, file);
  assert.strictEqual(fs.readFileSync(file, "utf8"), "hello");
  srv.close();
});

test("follows a redirect", async () => {
  const srv = await serve((req, res) => {
    if (req.url === "/a") {
      res.writeHead(302, { Location: "/b" });
      res.end();
    } else res.end("moved");
  });
  const file = tmp();
  await download(`http://127.0.0.1:${srv.address().port}/a`, file);
  assert.strictEqual(fs.readFileSync(file, "utf8"), "moved");
  srv.close();
});

test("rejects an HTTP error", async () => {
  const srv = await serve((req, res) => {
    res.statusCode = 404;
    res.end();
  });
  await assert.rejects(download(`http://127.0.0.1:${srv.address().port}/a`, tmp()), /HTTP 404/);
  srv.close();
});

test("rejects when the connection drops mid-body", async () => {
  const srv = await serve((req, res) => {
    res.writeHead(200, { "Content-Length": "1000" });
    res.write("x".repeat(100));
    setTimeout(() => res.socket.destroy(), 20);
  });
  await assert.rejects(download(`http://127.0.0.1:${srv.address().port}/a`, tmp()), /connection dropped|aborted|socket hang up/);
  srv.close();
});

test("exit code 1 from the agent fails the task, any other code does not", () => {
  assert.strictEqual(exitCodeFor(1), 1);
  for (const status of [0, 2, 127, null, undefined]) assert.strictEqual(exitCodeFor(status), 0, String(status));
});

test("reads the access token from the endpoint variable", () => {
  const endpoint = JSON.stringify({ parameters: { AccessToken: "from-endpoint" }, scheme: "OAuth" });
  assert.strictEqual(accessToken({ ENDPOINT_AUTH_SYSTEMVSSCONNECTION: endpoint }), "from-endpoint");
  // A mapped SYSTEM_ACCESSTOKEN wins; a missing or unreadable endpoint variable gives no token.
  assert.strictEqual(accessToken({ SYSTEM_ACCESSTOKEN: "mapped", ENDPOINT_AUTH_SYSTEMVSSCONNECTION: endpoint }), "mapped");
  assert.strictEqual(accessToken({}), "");
  assert.strictEqual(accessToken({ ENDPOINT_AUTH_SYSTEMVSSCONNECTION: "not json" }), "");
  assert.strictEqual(accessToken({ ENDPOINT_AUTH_SYSTEMVSSCONNECTION: "{}" }), "");
});
