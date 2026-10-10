// Verifies that the semantic-release dependency set in package.json can still
// produce a release: it drives the two plugins whose contract has broken before
// (commit-analyzer and release-notes-generator, each loading the
// conventionalcommits preset) through the exact plugin configuration in
// .releaserc.json, against a synthetic commit set.
//
// Why this exists: the release job is the only place npm dependencies are
// exercised, and it runs on pushes to main - after a Renovate bump has merged.
// On 2026-08-22 conventional-changelog-conventionalcommits 10.4.0 (which
// requires conventional-changelog-writer@9) met
// @semantic-release/release-notes-generator 14.1.1 (which ships writer@8), and
// releases failed with a handlebars "Missing helper" error while all PR checks
// had been green. This script reproduces the failing step - rendering release
// notes with the installed writer - so an incompatible bump goes red in PR CI
// instead of on main.
//
// That pair is how the tree runs today: package.json overrides the writer of
// release-notes-generator 14 with the writer@9 it pins (see
// hack/changelog-config.mjs). Without the override the 10.x preset renders through
// writer@8 to its header and empty sections, which this script fails on.
//
// Run via: make test-release-tooling (CI: the release-tooling job).

import { readFile } from "node:fs/promises";

const RELEASERC_URL = new URL("../.releaserc.json", import.meta.url);

// One commit per release type the analyzer must distinguish, subjects chosen
// so the rendered notes can be asserted on.
const COMMITS = [
  {
    hash: "1111111111111111111111111111111111111111",
    message: "fix(controller): requeue while the StatefulSet is progressing",
  },
  {
    hash: "2222222222222222222222222222222222222222",
    message: "feat(api): add an antiAffinity block",
  },
  {
    hash: "3333333333333333333333333333333333333333",
    message:
      "feat(api)!: rename the tls block\n\nBREAKING CHANGE: spec.tls moved",
  },
  {
    hash: "4444444444444444444444444444444444444444",
    message: "chore(deps): update dependency example to v2",
  },
];

const logger = {
  log: () => {},
  error: () => {},
  success: () => {},
  warn: () => {},
};

function fail(message, cause) {
  console.error(`FAIL: ${message}`);
  if (cause) console.error(cause);
  process.exit(1);
}

// Take the plugin configs from .releaserc.json itself so this check cannot
// drift from what the release job actually loads.
function pluginConfigFrom(releaserc, pluginName) {
  for (const entry of releaserc.plugins) {
    if (entry === pluginName) return {};
    if (Array.isArray(entry) && entry[0] === pluginName) return entry[1] ?? {};
  }
  return fail(`${pluginName} is not configured in .releaserc.json`);
}

const releaserc = JSON.parse(await readFile(RELEASERC_URL, "utf8"));

// Step 0: the writer override in package.json is a bridge, not a pin to keep.
// The generator asks for conventional-changelog-writer 8, the preset renders only
// through 9. Once the generator itself asks for the major the pinned writer has,
// the override does nothing but hide that, and it goes. Only a caret range is
// read; anything else is reported rather than guessed at.
const pkg = JSON.parse(
  await readFile(new URL("../package.json", import.meta.url), "utf8"),
);
const writerOverride =
  pkg.overrides?.["@semantic-release/release-notes-generator"]?.[
    "conventional-changelog-writer"
  ];
if (writerOverride) {
  const generatorPkg = JSON.parse(
    await readFile(
      new URL(
        "../node_modules/@semantic-release/release-notes-generator/package.json",
        import.meta.url,
      ),
      "utf8",
    ),
  );
  const asked = generatorPkg.dependencies?.["conventional-changelog-writer"] ?? "";
  const pinned = pkg.devDependencies?.["conventional-changelog-writer"] ?? "";
  const askedMajor = /^\^(\d+)\./.exec(asked)?.[1];
  if (askedMajor === undefined) {
    fail(
      `@semantic-release/release-notes-generator ${generatorPkg.version} asks for conventional-changelog-writer "${asked}", which this check cannot read - decide by hand whether the override in package.json is still needed`,
    );
  }
  if (askedMajor === pinned.split(".")[0]) {
    fail(
      `@semantic-release/release-notes-generator ${generatorPkg.version} asks for conventional-changelog-writer ${asked}, which the pinned ${pinned} satisfies - remove the override from package.json`,
    );
  }
}

const context = {
  commits: COMMITS,
  logger,
  cwd: process.cwd(),
  options: {
    repositoryUrl: "https://github.com/guided-traffic/mosquitto-operator.git",
  },
  lastRelease: { gitTag: "v1.0.0", gitHead: COMMITS[0].hash, version: "1.0.0" },
  nextRelease: { gitTag: "v2.0.0", gitHead: COMMITS[3].hash, version: "2.0.0" },
};

// Step 1: the analyzer must load the preset and classify the commit set.
const { analyzeCommits } = await import("@semantic-release/commit-analyzer");
const analyzerConfig = pluginConfigFrom(
  releaserc,
  "@semantic-release/commit-analyzer",
);

let releaseType;
try {
  releaseType = await analyzeCommits(analyzerConfig, context);
} catch (error) {
  fail("analyzeCommits threw - the commit-analyzer/preset pair is broken", error);
}
if (releaseType !== "major") {
  fail(
    `analyzeCommits returned "${releaseType}" for a commit set containing a BREAKING CHANGE - expected "major"`,
  );
}

// Step 2: the notes generator must render the notes with the writer it loads.
// This is the step that failed on 2026-08-22: the preset's handlebars template
// called a helper the installed conventional-changelog-writer does not
// register, so rendering threw "Missing helper".
const { generateNotes } = await import(
  "@semantic-release/release-notes-generator"
);
const notesConfig = pluginConfigFrom(
  releaserc,
  "@semantic-release/release-notes-generator",
);

let notes;
try {
  notes = await generateNotes(notesConfig, context);
} catch (error) {
  fail(
    "generateNotes threw - the release-notes-generator/preset/writer set is broken",
    error,
  );
}

// A render that silently drops sections is as broken as one that throws:
// assert the type sections and the commit subjects actually appear.
//
// The second group matters for a different failure. The first five needles all
// come from the conventionalcommits preset, so they still render when
// .github/release-template.hbs is disconnected from .releaserc.json - the
// template would simply stop being used, and this check would stay green while
// every release note silently lost its install instructions. Only the template
// itself can produce the strings below, so they are what proves it is wired in.
// The two that carry the version prove its placeholder was filled in.
const mustContain = [
  // From the preset.
  "Features",
  "Bug Fixes",
  "requeue while the StatefulSet is progressing",
  "add an antiAffinity block",
  "rename the tls block",
  // From .github/release-template.hbs, and from nowhere else.
  "Quality Gates",
  `docker pull guidedtraffic/mosquitto-operator:${context.nextRelease.version}`,
  "helm repo add mosquitto-operator",
  `--version ${context.nextRelease.version}`,
];
for (const needle of mustContain) {
  if (!notes.includes(needle)) {
    fail(
      `rendered release notes are missing "${needle}" - the writer/preset pair renders incomplete notes\n--- rendered notes ---\n${notes}`,
    );
  }
}

console.log("OK: release tooling renders release notes");
console.log(`    analyzeCommits -> ${releaseType}`);
console.log(`    generateNotes  -> ${notes.length} chars, all sections present`);
