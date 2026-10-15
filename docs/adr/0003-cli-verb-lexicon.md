# ADR 0003: The CLI verb lexicon

Status: Accepted

## Decision

Forge CI uses a shared vocabulary for common operations. It is a reserved set
of concepts, not an allowlist of every command the CLI may expose.

| Verb | Meaning |
|---|---|
| `validate` | Assert an input or state satisfies a rule; fail closed otherwise. |
| `plan` | Compute a typed plan from inputs. |
| `build` | Produce an artifact. |
| `sign` | Attach a cryptographic signature. |
| `attest` | Emit or attach an attestation. |
| `promote` | Move a verified digest to a tag or stage. |
| `rollback` | Undo an admitted promotion using its recorded state. |
| `publish` | Upload a finished artifact. |
| `report` | Render existing state for a person or summary sink. |

Commands outside these concepts use domain-appropriate names, such as
`container login`, `version bump`, or `container ledger add`. A signature check
uses `validate`; cryptographic verification does not require a second verb.

## Review and evolution

Review command names with their callers and documentation. A lexical denylist
would reject legitimate domain verbs without detecting structural API problems.
The generated [CLI reference](../cli-reference.md) is the command inventory.

New APIs should keep checks distinguishable from publication, and high-level
orchestration distinguishable from low-level primitives. In particular,
`container ledger` supplies generic operations while `container release-images`
and `container base-images` carry workflow-specific admission policy.

Further simplification of overlapping publication checks, version commands and
container orchestration needs an explicit API design and compatibility review.
Any accepted rename updates the engine, its delivery layers, documentation and
companion expectations together. External caller migrations are coordinated
separately with their maintainers.
