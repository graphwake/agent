// Run with: node --test azure-devops/GraphwakeUpload/   (Node 20+). Checks that download() never hangs.
"use strict";

const assert = require("node:assert");
const fs = require("node:fs");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { download } = require("./index.js");

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
