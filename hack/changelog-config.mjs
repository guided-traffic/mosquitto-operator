// The conventional-changelog config @semantic-release/release-notes-generator
// loads (see the `config` option in .releaserc.json): the conventionalcommits
// preset, with its main handlebars template replaced by the one checked into this
// repository, .github/release-template.hbs. Everything else - the header and commit
// partials, the commit transform, the group ordering - stays the preset's.
//
// Why a module instead of a `writerOpts.mainTemplate` entry in .releaserc.json:
// conventional-changelog-writer takes the template as a string, and .releaserc.json
// is JSON, which cannot read a file. Inlining the template there would leave
// .github/release-template.hbs unused while renovate.json still manages the Go
// badge inside it - a Renovate PR editing a file nothing renders.
//
// hack/verify-release-tooling.mjs drives the exact plugin config from
// .releaserc.json, so a template that stops rendering fails the release-tooling job
// on the pull request rather than the release on main.

import { readFile } from "node:fs/promises";
import createPreset from "conventional-changelog-conventionalcommits";

const TEMPLATE_URL = new URL("../.github/release-template.hbs", import.meta.url);

const mainTemplate = await readFile(TEMPLATE_URL, "utf8");

export default async function createChangelogConfig(config) {
  const preset = await createPreset(config);
  return {
    ...preset,
    writer: { ...preset.writer, mainTemplate },
  };
}
