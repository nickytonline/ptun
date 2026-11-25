#!/usr/bin/env node
// Runs the native ptun binary from the platform package npm installed (one of the optionalDependencies).
"use strict";

const { spawn } = require("node:child_process");

const platforms = {
  "darwin arm64": "@nickytonline/ptun-darwin-arm64",
  "darwin x64": "@nickytonline/ptun-darwin-x64",
  "linux arm64": "@nickytonline/ptun-linux-arm64",
  "linux x64": "@nickytonline/ptun-linux-x64",
  "win32 arm64": "@nickytonline/ptun-win32-arm64",
  "win32 x64": "@nickytonline/ptun-win32-x64",
};

const key = `${process.platform} ${process.arch}`;
const pkg = platforms[key];
if (!pkg) {
  console.error(`ptun: no prebuilt binary for ${key}. See https://github.com/nickytonline/ptun#install`);
  process.exit(1);
}

const exe = process.platform === "win32" ? "ptun.exe" : "ptun";
let bin;
try {
  bin = require.resolve(`${pkg}/bin/${exe}`);
} catch {
  console.error(
    `ptun: ${pkg} isn't installed. It may have been skipped (--no-optional or --omit=optional).\n` +
      "Reinstall without those flags, or see https://github.com/nickytonline/ptun#install",
  );
  process.exit(1);
}

const child = spawn(bin, process.argv.slice(2), { stdio: "inherit" });
// Pass every signal on, so ptun cleans up its tunnel even when only this wrapper is signalled
// (kill, concurrently, a process manager). On Ctrl-C ptun gets SIGINT twice, once from the terminal
// and once from here; it ignores the second while it cleans up. Stay alive until ptun exits.
for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(sig, () => {
    if (child.exitCode === null && child.signalCode === null) child.kill(sig);
  });
}
child.on("error", (err) => {
  console.error(`ptun: ${err.message}`);
  process.exit(1);
});
child.on("exit", (code, signal) => {
  if (signal) {
    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 1);
});
