# Question catalog — consumed

The catalog of 2026-09-01 was put to the owner one question per turn and closed on 2026-10-05.
Every question in the scope of the first release became a record or an amendment in place:
[ADR 0012](../adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md),
[ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md),
[ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md),
and amendments of [ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md),
[ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md),
[ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md),
[ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) and
[ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md),
all indexed in [docs/adr/README.md](../adr/README.md). The measurements the answers rest on are in
[docs/developer/broker-behaviour.md](../developer/broker-behaviour.md). The questions about high
availability were not answered; they wait, renumbered HA1–HA7, in [ha-research.md](ha-research.md).

Nothing is added here. A new open decision lives in a ticket's `## Open questions` section
([docs/tickets/README.md](../tickets/README.md)). This file stays only so that the records'
references to "the catalog" resolve; it is deleted with this directory
([ADR 0011](../adr/0011-documentation-has-five-homes-and-tickets-are-work-lists-that-get-archived.md) D10).
