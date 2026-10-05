---
id: T5
title: the failed condition reveals objects the reader may not list
state: dropped
severity: low
security: boundary
threat: a subject who may read Mosquitto status in namespace N, but not ConfigMaps, Services or StatefulSets there, learns that an object with a given derived name exists and who does not own it, by creating a Mosquitto of that name and reading its Ready condition
urgency: later        # rule 4: decided, cheap
effort: XS
blocked-by:
filed-from: the documentation restructuring of 2026-10-05 (security pages)
opened: 2026-10-05
decided: 2026-10-05
done:
publication-accepted: 2026-10-05
dropped-reason: folded into the project plan, which is the work list (ADR 0011 D12)
---

## Current state

The existence oracle is public as H-16 on
[trust-boundaries.md](../../security/trust-boundaries.md#h-16) and accepted:
[ADR 0009](../../adr/0009-delete-only-through-owner-references.md) D5 keeps the precise message
`<Kind> <namespace>/<name> exists and is not owned by this Mosquitto`
([`mosquitto_controller.go:335`](../../../internal/controller/mosquitto_controller.go#L335)), copied
into the `Ready` condition ([`:106`](../../../internal/controller/mosquitto_controller.go#L106)). No
test pins the message's shape today.

## Required changes

1. A unit test that asserts the refusal message for each of the three kinds exactly, so a change
   to it is deliberate; observed failing once against an edited format string (ADR 0010).
2. Then `done`, with ADR 0009 D5's amendment as the extraction.
