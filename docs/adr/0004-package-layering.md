# ADR 0004: Internal package layering

Status: Accepted

## Dependency direction

Forge CI uses ports and adapters. The production layers are siblings under
`internal/`; dependency direction, rather than nesting, defines their boundaries.

```text
cli -> app -> domain <- adapters
```

| Package | Responsibility | Allowed dependencies |
|---|---|---|
| `domain` | Pure rules, values and the capabilities those rules require. | Domain packages and pure/shared utilities. |
| `app` | Use-case sequencing and policy across ports. | Domain packages and shared utilities. |
| `adapters` | Filesystem, subprocess, network and provider implementations. | Domain packages and shared utilities. |
| `cli` | Arguments, rendering and composition of applications with adapters. | All production layers. |

## Ports and shared code

A port is a small interface declared beside the code that consumes it. Adapters
satisfy it structurally; there is no central interface catalogue mirroring every
adapter method.

Shared utilities such as `cliio`, `pathsafe`, `safeexec`, `retry`, `archive`,
`pgp` and `runtimetags` may not depend on `app`, `cli` or `adapters`.
`testutil` and `livetest` are explicit test-infrastructure exceptions.

Libraries that operate on supplied bytes and values may be used directly by
domain/application code. Libraries that open files, start processes or call the
network belong at an effect boundary. For example, pure PGP verification belongs
in `internal/pgp`; the file-writing signer belongs in `adapters/openpgp`.
Environment-based provider detection belongs in `adapters/platform`.

## Enforcement

The `layer-*` depguard rules in `.golangci.yml` reject outward production imports.
`test/archguard` supplements them with effect and ownership checks. Build variants
excluded on the current host are covered by the variant-eligibility checks.

Tests are composition roots and may connect the real layers when the behavior
requires it. Repository-wide guards live in the guard packages listed in
[testing](../testing.md), with shared repository and flag helpers under
`internal/testutil`. New packages and effects must retain these boundaries.
