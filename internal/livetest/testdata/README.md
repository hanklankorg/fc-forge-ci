# Neutral target fixtures

Two accepted live-target contracts, version 4, that this repository's tests
project onto targets and run the destructive guard and the preflight over:

- `neutral-targets-v4-compose.json`: Forge Lab's compose road, with the two
  forges this tier drives, a gitea endpoint it does not, a CA and Fulcio.
- `neutral-targets-v4-github.json`: a hosted GitHub endpoint, null CA and no
  Fulcio; nothing this tier drives, so it exercises the clean skip.

The contract's definition -- the JSON Schema, the Go reader and the corpus of
accepted and refused documents pinned by hash -- lives in the shared
`forge-lab-contract` module (`codefloe.com/itiquette/forge-lab-contract`).
Refusals are tested there, not here.

Current cross-repository acceptance evidence is recorded in
[`docs/testing.md`](../../../docs/testing.md).
