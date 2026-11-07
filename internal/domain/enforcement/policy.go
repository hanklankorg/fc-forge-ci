// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package enforcement defines the administrator's check policy.
//
// policy.json, embedded when the engine is built, is the report-only default.
// Any of the four settings may be replaced at run time, and only as an
// explicit command-line value (see Admin); it is administrator policy where
// the administrator's entry point writes that command line. There
// is deliberately no policy path, environment binding or mutable setter, and
// an environment variable named like a setting is refused (see
// RefuseEnvironment) rather than ignored.
package enforcement

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

//go:embed policy.json
var embeddedPolicy []byte

// Check identifies an independently enforced class of findings.
type Check string

// Check classes controlled independently by the administrator.
const (
	Lint             Check = "lint"
	Vulnerabilities  Check = "vulnerabilities"
	ApplicationTests Check = "application-tests"
	// ReproducibleBuilds covers missing declared reproducible-build settings
	// (for example Maven's project.build.outputTimestamp). It never covers
	// unreadable or malformed build files.
	ReproducibleBuilds Check = "reproducible-builds"
)

// Checks lists every check class in the order the settings are documented.
var Checks = []Check{Lint, Vulnerabilities, ApplicationTests, ReproducibleBuilds}

// Setting is the administrator setting name controlling check, for example
// enforce-lint. It is also the engine's command-line flag name.
func Setting(check Check) string { return "enforce-" + string(check) }

// Source says where an effective setting came from.
type Source string

// The two sources a setting can have. A source names the mechanism, not
// who used it: a flag is whatever the command line that ran the engine
// said. That the command line is the administrator's is a property of the
// pipeline that wrote it, which the engine cannot see.
const (
	// EmbeddedDefault is the policy.json built into the engine.
	EmbeddedDefault Source = "embedded-default"
	// Flag is a value given to the engine as its --enforce-* flag.
	Flag Source = "flag"
)

// Policy is returned by value. Changing a copy cannot change the engine policy.
type Policy struct {
	EnforceLint             bool `json:"enforce-lint"`
	EnforceVulnerabilities  bool `json:"enforce-vulnerabilities"`
	EnforceApplicationTests bool `json:"enforce-application-tests"`
	// EnforceReproducibleBuilds is strict reproducible builds: missing
	// reproducible-build settings block the release.
	EnforceReproducibleBuilds bool `json:"enforce-reproducible-builds"`

	// admin marks the settings an administrator input replaced, one bit per
	// entry of Checks.
	admin uint8
}

// Admin holds the administrator's explicit settings, by check. An absent
// check keeps the embedded default. Values are admitted exactly: "true" or
// "false", nothing else.
type Admin map[Check]string

// WithAdmin returns the policy with the administrator's settings applied.
// Unknown checks and any value other than "true" or "false" are refused.
func (policy Policy) WithAdmin(admin Admin) (Policy, error) {
	for check, raw := range admin {
		index, known := checkIndex(check)
		if !known {
			return Policy{}, fmt.Errorf("unknown admin setting %q: %w", Setting(check), errs.ErrUsage)
		}

		value, err := ParseAdminValue(Setting(check), raw)
		if err != nil {
			return Policy{}, err
		}

		policy = withSetting(policy, check, value)
		policy.admin |= 1 << index
	}

	return policy, nil
}

// ParseAdminValue admits exactly "true" or "false" for setting. Spellings
// such as "1", "TRUE", "yes" or an empty value are refused, so a value that
// reached the engine is the value the administrator wrote.
func ParseAdminValue(setting, raw string) (bool, error) {
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be exactly true or false, not %q: %w", setting, raw, errs.ErrUsage)
	}
}

// Source reports whether check's effective setting is the embedded default
// or was given as a flag.
func (policy Policy) Source(check Check) Source {
	if index, known := checkIndex(check); known && policy.admin&(1<<index) != 0 {
		return Flag
	}

	return EmbeddedDefault
}

func checkIndex(check Check) (int, bool) {
	for index, known := range Checks {
		if known == check {
			return index, true
		}
	}

	return 0, false
}

// RefuseEnvironment refuses an environment that carries a variable named like
// a setting: ENFORCE_LINT, or any name ending in _ENFORCE_LINT such as
// FORGE_CI_ENFORCE_LINT, in any letter case, and likewise for the other
// checks. The engine never reads such a variable. On GitLab every project,
// group and instance variable reaches the job's environment, so one set there
// stops the gate instead of leaving the reader to wonder whether it applied.
// Forge-rendered action inputs (INPUT_ENFORCE-LINT) keep their hyphen and do
// not match.
func RefuseEnvironment(environ []string) error {
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)

		for _, check := range Checks {
			setting := strings.ToUpper(strings.ReplaceAll(Setting(check), "-", "_"))
			if upper == setting || strings.HasSuffix(upper, "_"+setting) {
				return fmt.Errorf("environment variable %s looks like the admin setting %s, which only the administrator's caller sets, as --%s on the command line; remove the variable: %w",
					name, Setting(check), Setting(check), errs.ErrUsage)
			}
		}
	}

	return nil
}

// Current reads only the bytes embedded when this engine was built: the
// default, before any administrator setting.
func Current() (Policy, error) { return decode(embeddedPolicy) }

// Digest identifies the exact default policy bytes carried by the engine.
func Digest() string {
	digest := sha256.Sum256(embeddedPolicy)

	return hex.EncodeToString(digest[:])
}

// Enforces refuses unknown check classes rather than treating them as optional.
func (policy Policy) Enforces(check Check) (bool, error) {
	switch check {
	case Lint:
		return policy.EnforceLint, nil
	case Vulnerabilities:
		return policy.EnforceVulnerabilities, nil
	case ApplicationTests:
		return policy.EnforceApplicationTests, nil
	case ReproducibleBuilds:
		return policy.EnforceReproducibleBuilds, nil
	default:
		return false, fmt.Errorf("unknown policy check %q: %w", check, errs.ErrUsage)
	}
}

// Decision describes findings separately from whether they block progression.
type Decision struct {
	Check         Check  `json:"check"`
	Findings      int    `json:"findings"`
	Enforced      bool   `json:"enforced"`
	Source        Source `json:"source"`
	PolicySHA256  string `json:"policy-sha256"`
	BlockingCount int    `json:"blocking-count"`
}

// Decide evaluates a policy value. Production gates obtain it from Current
// with the administrator's settings applied, never from consumer state.
func (policy Policy) Decide(check Check, count int) (Decision, error) {
	return policy.DecideCounts(check, count, count)
}

// DecideCounts separates observed findings from the subset eligible to block.
func (policy Policy) DecideCounts(check Check, total, eligible int) (Decision, error) {
	enforced, err := policy.Enforces(check)
	if err != nil {
		return Decision{}, err
	}

	if total < 0 || eligible < 0 || eligible > total {
		return Decision{}, fmt.Errorf("findings counts must satisfy 0 <= eligible <= total: %w", errs.ErrUsage)
	}

	return Decision{Check: check, Findings: total, Enforced: enforced, Source: policy.Source(check), PolicySHA256: Digest(), BlockingCount: eligible}, nil
}

// withSetting returns policy with one setting replaced.
func withSetting(policy Policy, check Check, value bool) Policy {
	switch check {
	case Lint:
		policy.EnforceLint = value
	case Vulnerabilities:
		policy.EnforceVulnerabilities = value
	case ApplicationTests:
		policy.EnforceApplicationTests = value
	case ReproducibleBuilds:
		policy.EnforceReproducibleBuilds = value
	}

	return policy
}

func decode(body []byte) (Policy, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))

	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return Policy{}, fmt.Errorf("admin policy must be an object: %w", errs.ErrInvalidConfig)
	}

	values := map[string]bool{}

	for decoder.More() {
		if err := readSetting(decoder, values); err != nil {
			return Policy{}, err
		}
	}

	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return Policy{}, fmt.Errorf("invalid admin policy object: %w", errs.ErrInvalidConfig)
	}

	if _, err = decoder.Token(); err != io.EOF {
		return Policy{}, fmt.Errorf("trailing admin policy data: %w", errs.ErrInvalidConfig)
	}
	// Omitted settings retain the documented report-only defaults.
	var policy Policy
	if value, set := values["enforce-lint"]; set {
		policy.EnforceLint = value
	}

	if value, set := values["enforce-vulnerabilities"]; set {
		policy.EnforceVulnerabilities = value
	}

	if value, set := values["enforce-application-tests"]; set {
		policy.EnforceApplicationTests = value
	}

	if value, set := values["enforce-reproducible-builds"]; set {
		policy.EnforceReproducibleBuilds = value
	}

	return policy, nil
}

func readSetting(decoder *json.Decoder, values map[string]bool) error {
	key, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read admin policy key: %w", errs.ErrInvalidConfig)
	}

	name, ok := key.(string)
	if !ok {
		return fmt.Errorf("invalid admin policy key: %w", errs.ErrInvalidConfig)
	}

	if _, duplicate := values[name]; duplicate {
		return fmt.Errorf("duplicate admin policy key %q: %w", name, errs.ErrInvalidConfig)
	}

	switch name {
	case "enforce-lint", "enforce-vulnerabilities", "enforce-application-tests", "enforce-reproducible-builds":
	default:
		return fmt.Errorf("unknown admin policy key %q: %w", name, errs.ErrInvalidConfig)
	}

	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("invalid admin policy value: %w", errs.ErrInvalidConfig)
	}

	boolean, ok := value.(bool)
	if !ok {
		return fmt.Errorf("admin policy %q must be boolean: %w", name, errs.ErrInvalidConfig)
	}

	values[name] = boolean

	return nil
}
