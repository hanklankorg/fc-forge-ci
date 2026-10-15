// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ghaenv_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/ghaenv"
)

func TestReaders_DeclarationBoundaries(t *testing.T) {
	e := ghaenv.Setup(t)
	payload := "version=payload\nwanted<<INNER\nnot an output\nINNER\nabsent=payload\nphantom<<INNER\ninside\nINNER"
	output := "message<<OUTER\n" + payload + "\nOUTER\n" +
		"version= real=version << literal \n" +
		"wanted<<END=marker\n\n first \nEND=marker suffix\nlast\n\nEND=marker\n" +
		"empty=\nempty-block<<STOP\nSTOP\nlast=no newline"
	require.NoError(t, os.WriteFile(e.OutputPath, []byte(output), 0o600))

	require.Equal(t, " real=version << literal ", e.Output("version"))
	require.Equal(t, "\n first \nEND=marker suffix\nlast\n", e.Multiline("wanted"))
	require.Equal(t, payload, e.Multiline("message"))
	require.Equal(t, "no newline", e.Output("last"))

	for _, key := range []string{"absent", "phantom", "missing", "message", "wanted", "empty", "empty-block"} {
		require.Empty(t, e.Output(key), key)
	}

	for _, key := range []string{"absent", "phantom", "missing", "version", "empty", "empty-block"} {
		require.Empty(t, e.Multiline(key), key)
	}
}

func TestReaders_LineEndingsAndLargeValues(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("%q", newline), func(t *testing.T) {
			env := ghaenv.Setup(t)
			large := strings.Repeat("x", 100_000)
			data := strings.Join([]string{"scalar=" + large, "block<<END", "", large, "", "END"}, newline)
			require.NoError(t, os.WriteFile(env.OutputPath, []byte(data), 0o600))
			require.Equal(t, large, env.Output("scalar"))
			require.Equal(t, newline+large+newline, env.Multiline("block"))
			require.Empty(t, env.Output("absent"))
			require.Empty(t, env.Multiline("absent"))
		})
	}
}

func TestReaders_LatestDeclaration(t *testing.T) {
	unrelated := "other<<OUTER\nkey=payload\nkey<<INNER\nnot a declaration\nINNER\nOUTER\n"
	for _, test := range []struct {
		name, data, scalar, multiline string
	}{
		{name: "scalar duplicate", data: "key=first\n" + unrelated + "key=last", scalar: "last"},
		{name: "heredoc duplicate", data: "key<<FIRST\nold\nFIRST\n" + unrelated + "key<<LAST\n\n last \nline\n\nLAST", multiline: "\n last \nline\n"},
		{name: "scalar to heredoc", data: "key=stale\n" + unrelated + "key<<LAST\nlatest\nLAST", multiline: "latest"},
		{name: "heredoc to scalar", data: "key<<FIRST\nstale\nFIRST\n" + unrelated + "key=latest", scalar: "latest"},
		{name: "empty scalar overwrite", data: "key=first\n" + unrelated + "key="},
		{name: "empty heredoc overwrite", data: "key<<FIRST\nold\nFIRST\n" + unrelated + "key<<EMPTY\nEMPTY"},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := ghaenv.Setup(t)
			data := test.data + "\n" + unrelated + "neighbor=kept\n"
			require.NoError(t, os.WriteFile(env.OutputPath, []byte(data), 0o600))
			require.Equal(t, test.scalar, env.Output("key"))
			require.Equal(t, test.multiline, env.Multiline("key"))
			require.Equal(t, "kept", env.Output("neighbor"))
			require.Empty(t, env.Output("missing"))
			require.Empty(t, env.Multiline("missing"))
		})
	}
}

func TestOutput_PreservesTerminalCR(t *testing.T) {
	env := ghaenv.Setup(t)
	require.NoError(t, os.WriteFile(env.OutputPath, []byte("key=value\r"), 0o600))
	require.Equal(t, "value\r", env.Output("key"))
}
