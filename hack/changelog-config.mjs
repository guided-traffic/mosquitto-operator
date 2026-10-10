// The conventional-changelog config @semantic-release/release-notes-generator
// loads (see the `config` option in .releaserc.json): the conventionalcommits
// preset, with its footer replaced by the one checked into this repository,
// .github/release-template.hbs. Everything above the footer - the header, the
// breaking-change notes, the commit groups and the commit transform - stays the
// preset's.
//
// Since conventionalcommits 10 the preset renders through
// conventional-changelog-writer 9, whose templates are functions and which has no
// handlebars and no mainTemplate any more. The file is therefore not compiled:
// {{version}} is its one placeholder, replaced here, and nothing else in it is
// interpreted. release-notes-generator 14 still asks for writer 8, so package.json
// overrides its writer with the one it pins; writer 8 renders this preset as a
// guard message instead of the notes.
//
// Why a module instead of a `writerOpts` entry in .releaserc.json: .releaserc.json
// is JSON, which can neither read a file nor hold a function. Inlining the footer
// there would leave .github/release-template.hbs unused while renovate.json still
// manages the Go badge inside it - a Renovate PR editing a file nothing renders.
//
// hack/verify-release-tooling.mjs drives the exact plugin config from
// .releaserc.json, so a footer that stops rendering fails the release-tooling job
// on the pull request rather than the release on main.

import { readFile } from "node:fs/promises";
import createPreset from "conventional-changelog-conventionalcommits";

const FOOTER_URL = new URL("../.github/release-template.hbs", import.meta.url);

const footer = await readFile(FOOTER_URL, "utf8");

export default async function createChangelogConfig(config) {
  const preset = await createPreset(config);
  return {
    ...preset,
    writer: {
      ...preset.writer,
      footerPartial: (context) => footer.replaceAll("{{version}}", context.version),
    },
  };
}
