# Open questions from the test review

No unresolved questions remain from this review. The three decisions were
rechecked against current code on 2026-10-11 and recorded in their durable homes:

- [Optional distro-package signing](DEVELOPMENT.md#optional-distro-package-signing):
  retain valid zero-package results; expected build outputs belong to the consumer's
  build/assembly checks, not the signing toggle.
- [Context-specific path checks](DEVELOPMENT.md#context-specific-path-checks):
  retain the shared lexical primitive and distinct caller contracts. Further
  unification needs a concrete compatibility requirement.
- [Subprocess credential policy](threat-model.md#subprocess-credential-policy):
  retain tool/mode-specific selection and its documented limits. A universal
  allowlist is rejected; concrete unnecessary credentials warrant targeted fixes.

These are decisions to preserve existing behavior, not newly implemented runtime
fixes or claims of complete credential isolation. Add a new question only with a
specific unresolved requirement or reproducible finding; Git history retains the
earlier analysis.
