// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Command my-cli greets the name it is given.
package main

import (
	"fmt"
	"os"
)

// The release build sets these with -ldflags.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	name := ""
	if len(os.Args) > 1 {
		name = os.Args[1]
	}

	if name == "--version" {
		fmt.Printf("my-cli %s (%s)\n", version, commit)

		return
	}

	fmt.Println(greeting(name))
}

func greeting(name string) string {
	if name == "" {
		name = "world"
	}

	return "Hello, " + name + "!"
}
