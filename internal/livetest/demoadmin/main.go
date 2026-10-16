// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Command demoadmin lists and deletes the demos `just demo` made, for
// `just demo-list` and `just demo-delete`:
//
//	demoadmin list <forgejo|gitlab|github> [org=<id>]
//	demoadmin delete <forgejo|gitlab|github> <name> [org=<id>] [confirm=<name>]
//
// On the lab it reads lab account demo's contract; on GitHub it runs inside
// Forge Lab's `just github run` for the organization ID given. A deletion
// shows exactly what goes and asks for the demo's name, typed or as confirm=.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

const usage = "usage: demoadmin list <forgejo|gitlab|github> [org=<id>]\n" +
	"       demoadmin delete <forgejo|gitlab|github> <name> [org=<id>] [confirm=<name>]"

// The two operations.
const (
	opList   = "list"
	opDelete = "delete"
)

// The forges a demo can be on, as the commands name them.
const (
	forgeForgejo = string(provider.ForgeForgejo)
	forgeGitLab  = string(provider.ForgeGitLab)
	forgeGitHub  = "github"
)

// errAborted is a deletion the operator did not confirm.
var errAborted = errors.New("not confirmed; nothing was deleted")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout)

	stop()

	if err != nil {
		fmt.Fprintf(os.Stderr, "demoadmin: %v\n", err)
		os.Exit(1)
	}
}

// request is one parsed command line.
type request struct {
	operation, forge, name, confirm string
	org                             int64
}

func parse(args []string) (request, error) {
	in, rest, err := parseCommand(args)
	if err != nil {
		return request{}, err
	}

	for _, word := range rest {
		if err := in.set(word); err != nil {
			return request{}, err
		}
	}

	if in.forge == forgeGitHub && in.org == 0 {
		return request{}, fmt.Errorf("github needs org=<numeric organization ID>: %w", errs.ErrUsage)
	}

	return in, nil
}

// parseCommand reads the operation, the forge and, for a deletion, the demo,
// and returns the settings after them.
func parseCommand(args []string) (request, []string, error) {
	if len(args) < 2 || (args[0] != opList && args[0] != opDelete) {
		return request{}, nil, fmt.Errorf("%s: %w", usage, errs.ErrUsage)
	}

	in, rest := request{operation: args[0], forge: args[1]}, args[2:]
	if in.operation == opDelete {
		if len(rest) == 0 {
			return request{}, nil, fmt.Errorf("%s: %w", usage, errs.ErrUsage)
		}

		in.name, rest = rest[0], rest[1:]
	}

	return in, rest, nil
}

// set takes one key=value setting.
func (in *request) set(word string) error {
	key, value, _ := strings.Cut(word, "=")

	switch {
	case key == "org" && in.forge == forgeGitHub:
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("org=%q is not a numeric organization ID: %w", value, errs.ErrUsage)
		}

		in.org = id
	case key == "confirm" && in.operation == opDelete && value != "":
		in.confirm = value
	default:
		return fmt.Errorf("unknown setting %q\n%s: %w", word, usage, errs.ErrUsage)
	}

	return nil
}

// demos is what the two operations need of a forge.
type demos interface {
	List(ctx context.Context) ([]livetest.DemoSet, error)
	Delete(ctx context.Context, set livetest.DemoSet) error
}

// labDemos is the demo account on one lab forge.
type labDemos struct{ target livetest.Target }

func (lab labDemos) List(ctx context.Context) ([]livetest.DemoSet, error) {
	return livetest.ListLabDemos(ctx, lab.target)
}

func (lab labDemos) Delete(ctx context.Context, set livetest.DemoSet) error {
	return livetest.DeleteLabDemo(ctx, lab.target, set)
}

func open(ctx context.Context, in request) (demos, func(), error) {
	switch in.forge {
	case forgeForgejo, forgeGitLab:
		target, err := livetest.AcceptDemoTarget(provider.ForgeAPI(in.forge), time.Now())
		if err != nil {
			return nil, nil, err
		}

		return labDemos{target: target}, func() {}, nil
	case forgeGitHub:
		hosted, err := livetest.OpenGitHubDemos(ctx, in.org)
		if err != nil {
			return nil, nil, err
		}

		return hosted, hosted.Close, nil
	}

	return nil, nil, fmt.Errorf("forge must be forgejo, gitlab or github, got %q: %w", in.forge, errs.ErrUsage)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	in, err := parse(args)
	if err != nil {
		return err
	}

	forge, closeForge, err := open(ctx, in)
	if err != nil {
		return err
	}

	defer closeForge()

	sets, err := forge.List(ctx)
	if err != nil {
		return err
	}

	if in.operation == opList {
		if len(sets) == 0 {
			_, _ = fmt.Fprintf(stdout, "no demos on %s\n", in.forge)
		}

		for _, set := range sets {
			show(stdout, set)
		}

		return nil
	}

	return remove(ctx, forge, sets, in, stdin, stdout)
}

func remove(ctx context.Context, forge demos, sets []livetest.DemoSet, in request, stdin io.Reader, stdout io.Writer) error {
	var target *livetest.DemoSet

	for index := range sets {
		if sets[index].Name == in.name {
			target = &sets[index]
		}
	}

	if target == nil {
		return fmt.Errorf("no demo %q on %s; `just demo-list %s` shows what there is: %w", in.name, in.forge, in.forge, errs.ErrValidation)
	}

	_, _ = fmt.Fprintln(stdout, "This deletes, for good:")
	show(stdout, *target)

	if err := confirmed(in, stdin, stdout); err != nil {
		return err
	}

	if err := forge.Delete(ctx, *target); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "deleted demo %s on %s\n", target.Name, in.forge)

	return afterwards(ctx, forge, in, stdout)
}

// confirmed requires the demo's name, from confirm= or typed.
func confirmed(in request, stdin io.Reader, stdout io.Writer) error {
	answer := in.confirm
	if answer == "" {
		_, _ = fmt.Fprintf(stdout, "Type the demo's name (%s) to delete it: ", in.name)

		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}

		answer = strings.TrimSpace(line)
	}

	if answer != in.name {
		return errAborted
	}

	return nil
}

// afterwards says what is left to do once the deletion is done.
func afterwards(ctx context.Context, forge demos, in request, stdout io.Writer) error {
	if in.forge == forgeGitHub {
		_, _ = fmt.Fprintln(stdout, "GitHub keeps a repository's container packages; delete any the demo published under the organization's Packages.")

		return nil
	}

	left, err := forge.List(ctx)
	if err != nil {
		return err
	}

	if len(left) == 0 {
		_, _ = fmt.Fprintf(stdout, "No demo is left on %s. When none is left on the other lab forge either, retire the account's token in Forge Lab:\n"+
			"  just targets-revoke ~/.local/state/forge-lab/targets/demo.json\n", in.forge)
	}

	return nil
}

func show(stdout io.Writer, set livetest.DemoSet) {
	_, _ = fmt.Fprintf(stdout, "%s\n", set.Name)

	for _, repository := range set.Repositories {
		_, _ = fmt.Fprintf(stdout, "  %-48s id %-8d %s\n", repository.Name, repository.ID, repository.WebURL)
	}
}
