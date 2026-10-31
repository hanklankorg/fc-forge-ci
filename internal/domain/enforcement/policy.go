// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package enforcement defines the administrator's build-time check policy.
// policy.json belongs to the independently controlled engine distribution. There
// is deliberately no runtime policy path, environment override, or mutable setter.
package enforcement

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

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

// Policy is returned by value. Changing a copy cannot change the engine policy.
type Policy struct {
	EnforceLint             bool `json:"enforce-lint"`
	EnforceVulnerabilities  bool `json:"enforce-vulnerabilities"`
	EnforceApplicationTests bool `json:"enforce-application-tests"`
	// EnforceReproducibleBuilds is strict reproducible builds: missing
	// reproducible-build settings block the release.
	EnforceReproducibleBuilds bool `json:"enforce-reproducible-builds"`
}

// Current reads only bytes embedded when the administrator built this engine.
func Current() (Policy, error) { return decode(embeddedPolicy) }

// Digest identifies the exact policy bytes carried by the immutable engine.
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
	Check        Check  `json:"check"`
	Findings     int    `json:"findings"`
	Enforced     bool   `json:"enforced"`
	PolicySHA256 string `json:"policy-sha256"`
}

// Evaluate accepts classified findings, never execution or verification errors.
func Evaluate(check Check, count int) (Decision, error) {
	policy, err := Current()
	if err != nil {
		return Decision{}, err
	}

	return policy.Decide(check, count)
}

// Decide evaluates a policy value; production gates obtain it only from Current.
func (policy Policy) Decide(check Check, count int) (Decision, error) {
	enforced, err := policy.Enforces(check)
	if err != nil {
		return Decision{}, err
	}

	if count < 0 {
		return Decision{}, fmt.Errorf("findings count must not be negative: %w", errs.ErrUsage)
	}

	return Decision{Check: check, Findings: count, Enforced: enforced, PolicySHA256: Digest()}, nil
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
	// Omitted settings retain the documented enforcing defaults.
	policy := Policy{EnforceLint: true, EnforceVulnerabilities: true, EnforceApplicationTests: true, EnforceReproducibleBuilds: true}
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
