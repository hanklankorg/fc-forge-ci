// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

// The serving repository is deliberately not named after the input: a fork
// that renames the repository, and a lab snapshot, are the cases the derived
// input name used to skip.
const (
	singlePinSubject = "example/delivery-0a1b"
	singlePinInput   = "forge-ci-sha"
	singlePinCommit  = "1111111111111111111111111111111111111111"
	singlePinOther   = "2222222222222222222222222222222222222222"
)

func singlePinWorkflow(withBlock string) string {
	return `jobs:
  build:
    steps:
      - run: make dist
  sign:
    needs: build
    uses: ` + singlePinSubject + `/.forgejo/workflows/sign.yml@` + singlePinCommit + "\n" + withBlock
}

func runIsolationThroughRoot(t *testing.T, workflow string, extra ...string) error {
	t.Helper()

	fsys := testfs.NewReal(t)
	path := fsys.WriteFile(".forgejo/workflows/release.yml", []byte(workflow))

	root := cli.New(cli.BuildInfo{Version: "x", Commit: "y", Date: "z"})

	argv := append([]string{"forge-ci", "validate", "isolation", "--workflow", path}, extra...)

	return root.Run(context.Background(), argv)
}

func TestIsolationCmd_SinglePinInput(t *testing.T) {
	pinFlags := []string{"--single-pin-subject", singlePinSubject, "--single-pin-input", singlePinInput}

	tests := []struct {
		name      string
		withBlock string
		flags     []string
		want      error
	}{
		{
			name:      "the input repeats the uses pin",
			withBlock: "    with:\n      " + singlePinInput + ": " + singlePinCommit + "\n",
			flags:     pinFlags,
		},
		{
			name:      "the input names another commit",
			withBlock: "    with:\n      " + singlePinInput + ": " + singlePinOther + "\n",
			flags:     pinFlags,
			want:      errs.ErrValidation,
		},
		{
			name:  "the call passes no such input",
			flags: pinFlags,
			want:  errs.ErrValidation,
		},
		{
			name:      "the input is computed",
			withBlock: "    with:\n      " + singlePinInput + ": ${{ vars.PIN }}\n",
			flags:     pinFlags,
			want:      errs.ErrValidation,
		},
		{
			name:      "an input named after the repository is not the stated one",
			withBlock: "    with:\n      delivery-0a1b-helpers-sha: " + singlePinCommit + "\n",
			flags:     pinFlags,
			want:      errs.ErrValidation,
		},
		{
			name:  "without the flag only uses pins are compared",
			flags: []string{"--single-pin-subject", singlePinSubject},
		},
		{
			name:      "the flag needs a subject",
			withBlock: "    with:\n      " + singlePinInput + ": " + singlePinCommit + "\n",
			flags:     []string{"--single-pin-input", singlePinInput},
			want:      errs.ErrUsage,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := runIsolationThroughRoot(t, singlePinWorkflow(testCase.withBlock), testCase.flags...)

			if testCase.want == nil {
				if err != nil {
					t.Fatalf("err = %v, want none", err)
				}

				return
			}

			if !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}

			if strings.Contains(err.Error(), singlePinOther) {
				t.Errorf("err = %v: the violations belong on the annotation stream, not in the error", err)
			}
		})
	}
}

const (
	literalImage  = "forge.example/owner/signer@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	literalDigest = "2222222222222222222222222222222222222222222222222222222222222222"
)

// A trust input named with --literal-input is written out in the workflow, in
// its shape, by every job that calls the subject and passes it. The public
// key itself may stay an expression: the gate holds only what it is told to.
func TestIsolationCmd_LiteralInput(t *testing.T) {
	literalFlags := []string{
		"--single-pin-subject", singlePinSubject,
		"--literal-input", "signer-image=image-digest",
		"--literal-input", "key-sha256=sha256",
		"--literal-input", "caller",
		"--literal-input", "log=enum:none/public",
	}
	with := func(lines ...string) string {
		return "    with:\n      " + strings.Join(lines, "\n      ") + "\n"
	}

	tests := []struct {
		name      string
		withBlock string
		flags     []string
		want      error
		message   string
	}{
		{
			name: "literals in their shapes pass beside an unnamed expression",
			withBlock: with("signer-image: "+literalImage, "key-sha256: "+literalDigest, "caller: release.yml",
				"public-key: ${{ vars.PUBLIC_KEY }}"),
			flags: literalFlags,
		},
		{
			name:  "a call that passes none of them is left to the called workflow",
			flags: literalFlags,
		},
		{
			name:      "an image from a variable is refused",
			withBlock: with("signer-image: ${{ vars.SIGNER_IMAGE }}"),
			flags:     literalFlags,
			want:      errs.ErrValidation,
			message:   `job "sign" passes signer-image to ` + singlePinSubject + ` as a value that is an expression`,
		},
		{
			name:      "a digest from a variable is refused",
			withBlock: with("key-sha256: ${{ vars.KEY_SHA256 }}"),
			flags:     literalFlags,
			want:      errs.ErrValidation,
			message:   "passes key-sha256 to " + singlePinSubject + " as a value that is an expression",
		},
		{
			name:      "an expression inside a literal is refused",
			withBlock: with("signer-image: forge.example/owner/signer@sha256:${{ vars.DIGEST }}"),
			flags:     literalFlags,
			want:      errs.ErrValidation,
			message:   "is an expression",
		},
		{
			name:      "an image pinned by tag is refused",
			withBlock: with("signer-image: forge.example/owner/signer:latest"),
			flags:     literalFlags,
			want:      errs.ErrValidation,
			message:   "is not an image reference pinned by digest",
		},
		{
			name:      "a placeholder digest is refused",
			withBlock: with("key-sha256: PIN_KEY_SHA256"),
			flags:     literalFlags,
			want:      errs.ErrValidation,
			message:   "is not a SHA-256 digest of 64 lowercase hex characters",
		},
		{
			name:      "an empty literal is refused",
			withBlock: with("caller: ''"),
			flags:     literalFlags,
			want:      errs.ErrValidation,
			message:   "passes caller to " + singlePinSubject + " as a value that is empty",
		},
		{
			name:      "each value of an enum passes",
			withBlock: with("log: public"),
			flags:     literalFlags,
		},
		{
			name:      "a value outside the enum is refused",
			withBlock: with("log: Public"),
			flags:     literalFlags,
			want:      errs.ErrValidation,
			message:   "passes log to " + singlePinSubject + " as a value that is not one of none, public",
		},
		{
			name:      "an enum value from a variable is refused",
			withBlock: with("log: ${{ vars.LOG }}"),
			flags:     literalFlags,
			want:      errs.ErrValidation,
			message:   "passes log to " + singlePinSubject + " as a value that is an expression",
		},
		{
			name:      "a mapping is not a literal",
			withBlock: with("caller:", "  name: release.yml"),
			flags:     literalFlags,
			want:      errs.ErrValidation,
			message:   "is not a scalar",
		},
		{
			name:      "the flag needs a subject",
			withBlock: with("signer-image: " + literalImage),
			flags:     []string{"--literal-input", "signer-image"},
			want:      errs.ErrUsage,
		},
		{
			name:  "an unknown shape is a usage error",
			flags: []string{"--single-pin-subject", singlePinSubject, "--literal-input", "signer-image=digest"},
			want:  errs.ErrUsage,
		},
		{
			name:  "an enum without values is a usage error",
			flags: []string{"--single-pin-subject", singlePinSubject, "--literal-input", "log=enum:"},
			want:  errs.ErrUsage,
		},
		{
			name:  "an enum with an empty value is a usage error",
			flags: []string{"--single-pin-subject", singlePinSubject, "--literal-input", "log=enum:none//public"},
			want:  errs.ErrUsage,
		},
		{
			name:  "an enum that lists a value twice is a usage error",
			flags: []string{"--single-pin-subject", singlePinSubject, "--literal-input", "log=enum:none/none"},
			want:  errs.ErrUsage,
		},
		{
			name:  "a name without a shape after the separator is a usage error",
			flags: []string{"--single-pin-subject", singlePinSubject, "--literal-input", "signer-image="},
			want:  errs.ErrUsage,
		},
		{
			name:  "a name given twice is a usage error",
			flags: []string{"--single-pin-subject", singlePinSubject, "--literal-input", "caller", "--literal-input", "caller=sha256"},
			want:  errs.ErrUsage,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			stderr, err := captureStderr(t, func() error {
				return runIsolationThroughRoot(t, singlePinWorkflow(testCase.withBlock), testCase.flags...)
			})

			if testCase.want == nil {
				if err != nil {
					t.Fatalf("err = %v, want none\n%s", err, stderr)
				}

				return
			}

			if !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}

			if !strings.Contains(stderr, testCase.message) {
				t.Errorf("stderr lacks %q:\n%s", testCase.message, stderr)
			}
		})
	}
}

// The check covers every workflow file of the directory, as the pin check
// does: a base-image workflow beside release.yml calls the same subject.
func TestIsolationCmd_LiteralInputCoversSiblingWorkflowFiles(t *testing.T) {
	fsys := testfs.NewReal(t)
	path := fsys.WriteFile(".forgejo/workflows/release.yml", []byte(singlePinWorkflow("")))
	fsys.WriteFile(".forgejo/workflows/bases.yml", []byte(`jobs:
  verify:
    uses: `+singlePinSubject+`/.forgejo/workflows/verify.yml@`+singlePinCommit+`
    with:
      signer-image: ${{ vars.SIGNER_IMAGE }}
  other:
    uses: example/elsewhere/.forgejo/workflows/verify.yml@`+singlePinOther+`
    with:
      signer-image: ${{ vars.SIGNER_IMAGE }}
`))

	stderr, err := captureStderr(t, func() error {
		return cli.New(cli.BuildInfo{Version: "x", Commit: "y", Date: "z"}).Run(context.Background(), []string{
			"forge-ci", "validate", "isolation", "--workflow", path,
			"--single-pin-subject", singlePinSubject, "--literal-input", "signer-image=image-digest",
		})
	})
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation\n%s", err, stderr)
	}

	if !strings.Contains(stderr, `job "verify" passes signer-image`) || strings.Contains(stderr, `job "other"`) {
		t.Errorf("want the sibling file's call to the subject reported and no other:\n%s", stderr)
	}
}

// captureStderr runs fn with the process's standard error in a pipe and
// returns what it wrote: the gate's violations go to the annotation stream,
// not into the error. The caller is not parallel.
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	saved := os.Stderr
	os.Stderr = writer

	written := make(chan string, 1)

	go func() {
		data, _ := io.ReadAll(reader)
		written <- string(data)
	}()

	runErr := fn()
	os.Stderr = saved

	_ = writer.Close()
	text := <-written
	_ = reader.Close()

	return text, runErr
}
