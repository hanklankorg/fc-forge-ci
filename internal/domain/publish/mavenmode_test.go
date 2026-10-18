// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/publish"
	"github.com/stretchr/testify/require"
)

func TestMavenReactorPublicationPolicy(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, body, expected string
		snapshot, valid      bool
	}{
		{"release", `<project><version>1.2.3</version></project>`, "1.2.3", false, true},
		{"wrong authorized version", `<project><version>1.2.4</version></project>`, "1.2.3", false, false},
		{"snapshot", `<project><version>1.2.3-SNAPSHOT</version></project>`, "", true, true},
		{"stable cannot use snapshot gate", `<project><version>1.2.3</version></project>`, "", true, false},
		{"prerelease cannot use snapshot gate", `<project><version>1.2.3-rc.1</version></project>`, "", true, false},
		{"all modules snapshots", `<projects><project><version>1.2.3-SNAPSHOT</version></project><project><version>2.0.0-SNAPSHOT</version></project></projects>`, "", true, true},
		{"late stable module", `<projects><project><version>1.2.3-SNAPSHOT</version></project><project><version>1.2.3</version></project></projects>`, "", true, false},
		{"missing version", `<project/>`, "", true, false},
		{"no projects", `<projects/>`, "", true, false},
		{"not a model", `<settings/>`, "", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := publish.ValidateMavenReactorVersions([]byte(test.body), test.snapshot, test.expected)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestSnapshotCanExplicitlyRequireCentralMetadata(t *testing.T) {
	t.Parallel()

	body := []byte(`<project><groupId>org.example</groupId><artifactId>fixture</artifactId><version>1.2.3-SNAPSHOT</version><packaging>pom</packaging></project>`)
	_, err := publish.ParseMavenSnapshot(body, "", false)
	require.NoError(t, err)
	_, err = publish.ParseMavenSnapshot(body, "", true)
	require.ErrorContains(t, err, "metadata")
}
