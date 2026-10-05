# Security policy

## Reporting a vulnerability

Report privately. Please do **not** open a public issue for a finding that lets someone read a
Secret, take over a broker's configuration or its traffic, or act with the operator's
ServiceAccount.

- Report through **GitHub private vulnerability reporting** on
  <https://github.com/guided-traffic/mosquitto-operator> (Security → Report a vulnerability):
  <https://github.com/guided-traffic/mosquitto-operator/security/advisories/new>.
- Or report to the maintainer organisation, <https://github.com/guided-traffic>.

> **Gap, stated plainly:** this repository publishes no contact address and no response time or
> disclosure window — stated rather than invented. Whether private vulnerability reporting is
> switched on for this repository is not verified here. If the form is not offered to you, the
> maintainer organisation is the remaining route.

Include the operator version — the `version` field of the operator's first log line,
`starting mosquitto-operator`, or the tag of its image — the chart version if you installed with
Helm, which install path you used (Helm or kustomize), whether `spec.tls` was set, and the relevant
part of `spec.config` if the finding involves it. Leave out credentials: a `spec.config` can carry
one, so redact it before you send it.

## What is already known

The security design, including the gaps it does not close, is documented under
[docs/security/](docs/security/). Anything written there is known — a report that adds a working
exploit, a wider consequence, or a case the analysis missed is still valuable. An open gap there
carries an `H-<n>` identifier in its heading; naming it in a report saves a round trip. Anything
that is **not** written there is what we most want to hear about.

## Supported versions

Releases are cut from `main` only: semantic-release is configured with `main` as its one release
branch ([.releaserc.json](.releaserc.json)), and there is no maintenance branch. A fix lands on
`main` and ships in the next release; there is no backport to an earlier release. Only the latest
release is supported. The project is on the `0.x` line; the first release is `0.1.0`, and the tag
`v0.0.0` on the initial commit is an empty placeholder that was never a version.
