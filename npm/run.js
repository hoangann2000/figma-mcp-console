#!/usr/bin/env node
// Chạy Go binary đi kèm trong gói npm — KHÔNG tải mạng/GitHub.
// stdout là transport MCP → mọi log của wrapper ra stderr.
"use strict";
const { spawnSync } = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");

const plat = process.platform === "win32" ? "windows" : process.platform; // darwin|linux|windows
const arch = { x64: "amd64", arm64: "arm64" }[process.arch];
function fail(m) {
  process.stderr.write(`figma-mcp-console: ${m}\n`);
  process.exit(1);
}
if (!arch || !["darwin", "linux", "windows"].includes(plat))
  fail(`unsupported platform: ${process.platform}/${process.arch}`);

const exe = plat === "windows" ? ".exe" : "";
const binPath = path.join(
  __dirname,
  "bin",
  `${plat}-${arch}`,
  `figma-mcp${exe}`,
);

// Lệnh con: install-plugin — trích plugin ra thư mục HIỆN RÕ (không có dấu chấm)
// và tự mở folder đó lên để user thấy ngay manifest.json.
if (process.argv[2] === "install-plugin") {
  const srcDir = path.join(__dirname, "bin", "plugin");
  const destDir = path.join(os.homedir(), "figma-mcp-console-plugin");
  fs.mkdirSync(destDir, { recursive: true });
  for (const f of ["code.js", "ui.html", "manifest.json"])
    fs.copyFileSync(path.join(srcDir, f), path.join(destDir, f));

  process.stdout.write(
    "\n✅ Figma plugin ready.\n\n" +
      "In Figma Desktop → Plugins → Development → Import plugin from manifest…\n" +
      "and select this file:\n\n" +
      "   " +
      path.join(destDir, "manifest.json") +
      "\n\n",
  );

  // Tự mở folder trong trình quản lý file (best-effort; lỗi thì bỏ qua).
  const opener =
    plat === "darwin" ? "open" : plat === "windows" ? "explorer" : "xdg-open";
  try {
    spawnSync(opener, [destDir], { stdio: "ignore", detached: true });
  } catch (_) {}
  process.exit(0);
}

if (!fs.existsSync(binPath)) fail(`không tìm thấy binary cho ${plat}-${arch}`);
try {
  fs.chmodSync(binPath, 0o755);
} catch (_) {}
const r = spawnSync(binPath, process.argv.slice(2), { stdio: "inherit" });
if (r.error) fail(`không khởi động được server: ${r.error.message}`);
process.exit(r.status === null ? 1 : r.status);
