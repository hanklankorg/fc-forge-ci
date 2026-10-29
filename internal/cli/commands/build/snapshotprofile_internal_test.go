// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"github.com/stretchr/testify/require"
)

func TestSnapshotMavenProfileAppliesToEveryPhaseAsOneArgument(t *testing.T) {
	for _, kind := range []string{"app", "lib"} {
		t.Run(kind, func(t *testing.T) {
			env := testenv.New(t)
			t.Chdir(env.Temp)
			require.NoError(t, os.WriteFile("pom.xml", []byte(`<project><modelVersion>4.0.0</modelVersion><groupId>fixture</groupId><artifactId>fixture</artifactId><version>1.2.3</version></project>`), 0o600))
			bin := env.MkdirAll("bin")
			executable, err := os.Executable()
			require.NoError(t, err)
			// The fixture only dispatches to the Go argv recorder below; it performs
			// no Maven operations or workflow decisions.
			require.NoError(t, os.WriteFile(filepath.Join(bin, "mvn"), []byte("#!/bin/sh\nexec \""+executable+"\" -test.run=TestSnapshotMavenArgvHelper -- \"$@\"\n"), 0o700))
			env.Setenv("PATH", bin+":/usr/bin:/bin")
			env.Setenv("FORGE_CI_MAVEN_HELPER", "1")
			env.Setenv("FORGE_CI_MAVEN_LOG", env.Path("maven-argv.jsonl"))
			require.NoError(t, mavenRunCmd().Run(t.Context(), []string{"run", "--working-dir", ".", "--build-type", kind, "--profile", "one, two", "--build-sbom=false"}))
			body, err := os.ReadFile(env.Path("maven-argv.jsonl"))
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSpace(string(body)), "\n")
			require.GreaterOrEqual(t, len(lines), 2)
			for _, line := range lines {
				var args []string
				require.NoError(t, json.Unmarshal([]byte(line), &args))
				index := slices.Index(args, "-P")
				require.GreaterOrEqual(t, index, 0)
				require.Equal(t, "one, two", args[index+1])
				require.Equal(t, 1, strings.Count(strings.Join(args, "\x00"), "-P"))
			}
		})
	}
}

func TestSnapshotMavenArgvHelper(t *testing.T) {
	if os.Getenv("FORGE_CI_MAVEN_HELPER") != "1" {
		return
	}
	index := slices.Index(os.Args, "--")
	if index < 0 {
		os.Exit(2)
	}
	body, err := json.Marshal(os.Args[index+1:])
	if err != nil {
		os.Exit(2)
	}
	file, err := os.OpenFile(os.Getenv("FORGE_CI_MAVEN_LOG"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(2)
	}
	if _, err = file.Write(append(body, '\n')); err != nil {
		os.Exit(2)
	}
	if file.Close() != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
