---
id: T3
title: a mosquitto writer can mount any secret of its namespace into an image of its choice
state: dropped
severity: high
security: boundary
threat: a subject allowed to create or update a Mosquitto in namespace N, but not to read Secrets or create pods there, reads every Secret of N (and acts as any ServiceAccount whose legacy token Secret exists in N) through the StatefulSet the operator writes on its behalf
urgency: next         # rule 3: severity high, trigger live in the default configuration
effort: M
blocked-by:
filed-from: the documentation restructuring of 2026-10-05 (security pages)
opened: 2026-10-05
decided: 2026-10-05
done:
publication-accepted: 2026-10-05
dropped-reason: folded into the project plan, which is the work list (ADR 0011 D12)
---

## Current state

The gap is public as H-15 on [trust-boundaries.md](../../security/trust-boundaries.md#h-15), by the
owner's decision before a switch exists. `spec.tls.secretName` may name any Secret of the
namespace and is mounted whole ([`statefulset.go:129-139`](../../../internal/builder/statefulset.go#L129-L139));
`spec.image` is free, and the container runs whatever the image puts at `/usr/sbin/mosquitto`. The
decided `credentialsSecret` of [ADR 0013](../../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md)
D2 would widen the same pattern: the operator would hash any Secret's value of the namespace into
`<name>-auth`, and MQTT logins would become an online oracle for that value.

The decision is [ADR 0014](../../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)
D10, with amendments in [ADR 0001](../../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)
D1/D6 and ADR 0013 D2: an install-time switch `secretSecurity`, default `false` (the owner's choice
against the recommended `true`). With `true` only Secrets carrying an opt-in label may be named.
Nothing of it is built.

The image half stays with the cluster: [ADR 0007](../../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md)
D9 leaves image policy to admission control, and H-2 says so.

## Required changes

1. The operator flag and the chart value `secretSecurity`, default `false` on both install paths;
   with `true`: the label check for the TLS Secret (labels only, never content) and for every
   `credentialsSecret`, `Ready=False` with a reason naming the label, the Secret cache restricted
   to labelled Secrets. The label key fixed under `mko.gtrfc.com/` and named in the README.
2. Tests: a unit test that an unlabelled TLS Secret and an unlabelled `credentialsSecret` are
   refused with `true` and accepted with `false`; an integration test that the flag reaches the
   operator from both install paths; observed failing once with the check removed (ADR 0010).
3. README: the switch in the Helm values table and the operator flags, and the trust rule of
   `false` next to the install command. H-15 rewritten in the same change to name the switch as
   the mitigation, and the Status of ADR 0014, 0001 and 0013 set to built for D10.
4. Until the `MosquittoUser` kind exists, step 1 covers the TLS Secret alone; the
   `credentialsSecret` half lands with that kind.
