#!/usr/bin/env node
// Thin npx wrapper: downloads the figma-mcp Go binary for this platform from
// GitHub Releases (once, cached in the home directory) and executes it.
//
// IMPORTANT: stdout is the MCP JSON-RPC transport — every message from this
// wrapper must go to stderr.

"use strict";

const { spawnSync } = require("child_process");
const fs = require("fs");
const https = require("https");
const os = require("os");
const path = require("path");

const pkg = require("./package.json");
const REPO = "hoangann2000/figma-mcp-console"; // must match the GitHub repo

// The GitHub release tag to download the server binary from. Decoupled from
// pkg.version so npm-only updates (README, wrapper fixes) don't require a new
// GitHub release. Bump this ONLY when a new vX.Y.Z release exists on GitHub.
const BINARY_VERSION = "0.3.0";

const GOOS = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform];
const GOARCH = { x64: "amd64", arm64: "arm64" }[process.arch];

function fail(msg) {
  process.stderr.write(`figma-mcp-console: ${msg}\n`);
  process.exit(1);
}

if (!GOOS || !GOARCH) fail(`unsupported platform: ${process.platform}/${process.arch}`);
if (GOOS === "windows" && GOARCH === "arm64") fail("windows/arm64 builds are not published");

const exe = GOOS === "windows" ? ".exe" : "";
const asset = `figma-mcp_${GOOS}_${GOARCH}${exe}`;
const url = `https://github.com/${REPO}/releases/download/v${BINARY_VERSION}/${asset}`;
const cacheDir = path.join(os.homedir(), ".figma-mcp-console", BINARY_VERSION);
const binPath = path.join(cacheDir, `figma-mcp${exe}`);

function download(from, dest, redirects, done) {
  if (redirects > 5) return done(new Error("too many redirects"));
  https
    .get(from, { headers: { "User-Agent": "figma-mcp-console-npx" } }, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        res.resume();
        return download(res.headers.location, dest, redirects + 1, done);
      }
      if (res.statusCode !== 200) {
        res.resume();
        return done(new Error(`GET ${from} returned ${res.statusCode}`));
      }
      const file = fs.createWriteStream(dest, { mode: 0o755 });
      res.pipe(file);
      file.on("finish", () => file.close(done));
      file.on("error", done);
    })
    .on("error", done);
}

function run() {
  const result = spawnSync(binPath, process.argv.slice(2), { stdio: "inherit" });
  if (result.error) fail(`failed to start server: ${result.error.message}`);
  process.exit(result.status === null ? 1 : result.status);
}

if (fs.existsSync(binPath)) {
  run();
} else {
  process.stderr.write(`figma-mcp-console: downloading ${asset} v${BINARY_VERSION}...\n`);
  fs.mkdirSync(cacheDir, { recursive: true });
  // Download to a temp name, then rename, so a concurrently started client
  // never executes a half-written binary.
  const tmp = `${binPath}.${process.pid}.tmp`;
  download(url, tmp, 0, (err) => {
    if (err) {
      try { fs.unlinkSync(tmp); } catch (_) {}
      fail(`download failed: ${err.message}\n  from: ${url}\n  Check your network, or build from source (see the repo README).`);
    }
    try {
      fs.renameSync(tmp, binPath);
    } catch (e) {
      // Another process may have won the race; that's fine if the file exists.
      try { fs.unlinkSync(tmp); } catch (_) {}
      if (!fs.existsSync(binPath)) fail(`could not install binary: ${e.message}`);
    }
    process.stderr.write("figma-mcp-console: ready\n");
    run();
  });
}
