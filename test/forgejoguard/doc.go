// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package forgejoguard asserts structural invariants of the Forgejo delivery
// layer: the reusable workflows under .forgejo/workflows, the composite
// actions under .forgejo/actions, the consumer examples under
// examples/<ecosystem>/forgejo and the signer-image example under
// examples/forgejo. It holds the signing boundary (which job receives which
// secret, the signer bootstrap, the coordinate preflight), consumer routing,
// strict YAML, that no file names an organization, and that no workflow can
// start without a caller. The coordinate preflight scripts are also run here,
// against fixed inputs. It is a test-only package and never executes the
// engine; test/forgejobehavior runs the layer's shell.
//
// The invariants are read from the parsed document, not from the file text,
// so they depend on the structure they are about and not on indentation or
// line spelling. The parser is goccy/go-yaml's AST API, which keeps block
// scalars and comments apart from values; test/workflowguard, the GitHub
// layer's guard, has its own reader for its own rules.
package forgejoguard
