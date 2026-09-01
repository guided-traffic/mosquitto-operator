// Verifies that every Renovate customManager in renovate.json still points at
// something real: that its managerFilePatterns select at least one file in this
// repository, and that its matchStrings actually match inside those files.
//
// Why this exists: a customManager whose regex matches nothing fails silently.
// Renovate reports no error, the manager simply contributes no dependency, and
// the pin it was supposed to keep current stops moving. That is not
// hypothetical here - the Go version lives in four places (go.mod,
// Containerfile, GO_VERSION in .github/workflows/*.yml and the go-x.y-blue
// badge in .github/release-template.hbs) and is grouped into ONE Renovate PR so
// the four cannot drift apart. A manager silently matching nothing drops one of
// the four out of that group, and the drift only shows up much later, as a CI
// job building with a Go version the go.mod no longer asks for.
//
// The check is deliberately strict: zero matched files, zero matches inside the
// matched files, or a match that captures no currentValue are all failures.
//
// Caveat this script cannot cover: Renovate evaluates these patterns with RE2,
// Node with its own engine. RE2 rejects lookaround and backreferences, which
// Node accepts. Keep the patterns in renovate.json to plain regex constructs;
// this script proves that a pattern matches, not that RE2 can compile it.
//
// Run via: make verify-ci-references (CI: the release-tooling job).

import { readFile, readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const REPO_ROOT = fileURLToPath(new URL("..", import.meta.url));

// Directories that hold no source of truth for a pin: build output, vendored or
// installed dependencies, and the git database itself.
const SKIP_DIRS = new Set([
  ".git",
  "bin",
  "coverage",
  "node_modules",
  "tmp",
  "vendor",
]);

const problems = [];

function fail(message) {
  problems.push(message);
}

/** Every tracked-looking file in the repo, as a POSIX path relative to the root. */
async function listFiles(dir = REPO_ROOT, prefix = "") {
  const files = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) {
      if (SKIP_DIRS.has(entry.name)) continue;
      files.push(...(await listFiles(path.join(dir, entry.name), rel)));
    } else if (entry.isFile()) {
      files.push(rel);
    }
  }
  return files;
}

// Renovate accepts two forms in managerFilePatterns: a regex delimited by
// slashes, and a glob. Everything in this repo uses the regex form; a bare
// string is treated as a literal path so a plain "Makefile" still works.
function matcherFor(pattern, managerLabel) {
  if (pattern.length > 1 && pattern.startsWith("/")) {
    const end = pattern.lastIndexOf("/");
    if (end > 0) {
      const flags = pattern.slice(end + 1);
      const body = pattern.slice(1, end);
      try {
        const re = new RegExp(body, flags);
        return (file) => re.test(file);
      } catch (error) {
        fail(`${managerLabel}: managerFilePatterns entry ${pattern} is not a valid regex: ${error.message}`);
        return () => false;
      }
    }
  }
  if (/[*?[\]{}]/.test(pattern)) {
    fail(
      `${managerLabel}: managerFilePatterns entry ${pattern} looks like a glob. ` +
        "This check only understands /regex/ patterns and literal paths - " +
        "rewrite it as a regex or teach hack/verify-ci-references.mjs globs.",
    );
    return () => false;
  }
  return (file) => file === pattern;
}

const renovate = JSON.parse(
  await readFile(path.join(REPO_ROOT, "renovate.json"), "utf8"),
);

const managers = renovate.customManagers ?? [];
if (managers.length === 0) {
  fail("renovate.json declares no customManagers - nothing to verify");
}

const repoFiles = await listFiles();

for (const [index, manager] of managers.entries()) {
  const label = manager.description
    ? `customManagers[${index}] "${manager.description}"`
    : `customManagers[${index}]`;
  const problemsBefore = problems.length;

  const patterns = manager.managerFilePatterns ?? [];
  if (patterns.length === 0) {
    fail(`${label}: has no managerFilePatterns`);
    continue;
  }
  const matchStrings = manager.matchStrings ?? [];
  if (matchStrings.length === 0) {
    fail(`${label}: has no matchStrings`);
    continue;
  }

  const matchers = patterns.map((p) => matcherFor(p, label));
  const files = repoFiles.filter((file) => matchers.some((m) => m(file)));

  if (files.length === 0) {
    fail(
      `${label}: managerFilePatterns ${JSON.stringify(patterns)} match no file in the repository`,
    );
    continue;
  }

  // Every matchString has to hit somewhere across the selected files. A single
  // selected file with zero hits is NOT an error: a directory pattern such as
  // /^\.github/workflows/.*\.ya?ml$/ legitimately also selects renovate.yml,
  // which carries no GO_VERSION. The per-file counts are printed either way so
  // a zero is visible to a reader.
  const hitsPerFile = new Map(files.map((file) => [file, 0]));
  for (const matchString of matchStrings) {
    let re;
    try {
      re = new RegExp(matchString, "g");
    } catch (error) {
      fail(`${label}: matchString ${JSON.stringify(matchString)} is not a valid regex: ${error.message}`);
      continue;
    }

    let total = 0;
    for (const file of files) {
      const content = await readFile(path.join(REPO_ROOT, file), "utf8");
      re.lastIndex = 0;
      for (const match of content.matchAll(re)) {
        total += 1;
        hitsPerFile.set(file, hitsPerFile.get(file) + 1);
        // Without a currentValue Renovate has no version to compare or replace,
        // so a matching-but-valueless manager is still an inert one.
        if (!match.groups?.currentValue) {
          fail(
            `${label}: matchString ${JSON.stringify(matchString)} matched in ${file} but captured no currentValue`,
          );
        }
      }
    }

    if (total === 0) {
      fail(
        `${label}: matchString ${JSON.stringify(matchString)} matches no line in ${JSON.stringify(files)}`,
      );
    }
  }

  const summary = [...hitsPerFile]
    .map(([file, hits]) => `${file} (${hits})`)
    .join(", ");
  const status = problems.length === problemsBefore ? "OK" : "BAD";
  console.log(`${status}: ${label}\n    ${summary}`);
}

if (problems.length > 0) {
  console.error(
    `\nFAIL: ${problems.length} Renovate customManager problem(s) in renovate.json:`,
  );
  for (const problem of problems) console.error(`  - ${problem}`);
  process.exit(1);
}

console.log(`\nOK: all ${managers.length} Renovate customManagers reference real files and lines`);
