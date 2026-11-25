#!/usr/bin/env node
// Builds the npm packages from GoReleaser's dist/ and publishes them.
//
//   node scripts/npm-publish.mjs              # build into dist/npm and publish (CI, after goreleaser release)
//   node scripts/npm-publish.mjs --dry-run    # build and run `npm publish --dry-run`
//
// One package per platform (@nickytonline/ptun-<os>-<cpu>, with os/cpu set so npm only installs the matching one)
// and the `ptun` package, whose bin shim runs the binary from whichever platform package is installed.
// The platform packages are published first, so `ptun` never points at a version that doesn't exist yet.
import { execFileSync } from "node:child_process";
import { chmodSync, copyFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const root = new URL("..", import.meta.url).pathname;
const dist = join(root, "dist");
const out = join(dist, "npm");
const dryRun = process.argv.includes("--dry-run");

if (!dryRun && process.env.GITHUB_ACTIONS) {
  // Trusted publishing (OIDC) needs npm 11.5.1 or later.
  const [major, minor, patch] = execFileSync("npm", ["--version"], { encoding: "utf8" }).trim().split(".").map(Number);
  if (major < 11 || (major === 11 && (minor < 5 || (minor === 5 && patch < 1)))) {
    throw new Error(`npm ${major}.${minor}.${patch} can't use trusted publishing; use npm 11.5.1 or later`);
  }
}

const { version, tag } = JSON.parse(readFileSync(join(dist, "metadata.json"), "utf8"));
const artifacts = JSON.parse(readFileSync(join(dist, "artifacts.json"), "utf8"));
const prerelease = version.includes("-");
const distTag = prerelease ? "next" : "latest";

const goToNode = {
  os: { darwin: "darwin", linux: "linux", windows: "win32" },
  arch: { amd64: "x64", arm64: "arm64" },
};

const wrapper = JSON.parse(readFileSync(join(root, "npm/ptun/package.json"), "utf8"));
const common = {
  version,
  license: wrapper.license,
  homepage: wrapper.homepage,
  bugs: wrapper.bugs,
  repository: wrapper.repository,
  publishConfig: wrapper.publishConfig,
};

rmSync(out, { recursive: true, force: true });

const platformPkgs = [];
for (const a of artifacts) {
  if (a.type !== "Binary" || a.extra?.ID !== "ptun") continue;
  const os = goToNode.os[a.goos];
  const cpu = goToNode.arch[a.goarch];
  if (!os || !cpu) continue;
  const name = `@nickytonline/ptun-${os}-${cpu}`;
  const dir = join(out, `ptun-${os}-${cpu}`);
  const exe = os === "win32" ? "ptun.exe" : "ptun";
  mkdirSync(join(dir, "bin"), { recursive: true });
  copyFileSync(join(root, a.path), join(dir, "bin", exe));
  chmodSync(join(dir, "bin", exe), 0o755);
  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify(
      {
        name,
        ...common,
        description: `The ${os}-${cpu} binary for ptun. Install ptun instead.`,
        os: [os],
        cpu: [cpu],
        files: [`bin/${exe}`],
        preferUnplugged: true,
      },
      null,
      2,
    ) + "\n",
  );
  platformPkgs.push({ name, dir });
}
if (platformPkgs.length === 0) {
  throw new Error("no ptun binaries in dist/artifacts.json; run goreleaser first");
}

const wrapperDir = join(out, "ptun");
mkdirSync(join(wrapperDir, "bin"), { recursive: true });
copyFileSync(join(root, "npm/ptun/bin/ptun.js"), join(wrapperDir, "bin/ptun.js"));
chmodSync(join(wrapperDir, "bin/ptun.js"), 0o755);
copyFileSync(join(root, "README.md"), join(wrapperDir, "README.md"));
copyFileSync(join(root, "LICENSE"), join(wrapperDir, "LICENSE"));
writeFileSync(
  join(wrapperDir, "package.json"),
  JSON.stringify(
    {
      ...wrapper,
      version,
      optionalDependencies: Object.fromEntries(platformPkgs.map((p) => [p.name, version])),
    },
    null,
    2,
  ) + "\n",
);

// Already on npm (a re-run after a partial failure)? npm publish would fail, so skip it.
const published = (name) => {
  try {
    return execFileSync("npm", ["view", `${name}@${version}`, "version"], { encoding: "utf8" }).trim() === version;
  } catch {
    return false; // E404: the package or version doesn't exist yet
  }
};

const publish = (dir) => {
  const { name } = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
  if (!dryRun && published(name)) {
    console.log(`${name}@${version} is already published, skipping`);
    return;
  }
  // publishConfig sets access and provenance. Provenance only works in CI, which also stops a publish from a laptop.
  const args = ["publish", dir, "--tag", distTag];
  if (dryRun) args.push("--dry-run", "--provenance=false");
  execFileSync("npm", args, { stdio: "inherit" });
};

console.log(`Publishing ptun ${version} (${tag}) to npm with dist-tag ${distTag}${dryRun ? " (dry run)" : ""}`);
for (const p of platformPkgs) publish(p.dir);
publish(wrapperDir);
