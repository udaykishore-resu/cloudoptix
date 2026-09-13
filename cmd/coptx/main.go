// Command coptx is CloudOptix's command-line tool: the one CI pipelines and
// operators run.
//
// Everything it does, it does locally and in-process — it never talks to a
// running CloudOptix API. That is deliberate. `coptx cost test` is meant to
// gate a pull request in a repository that may have no CloudOptix deployment
// at all, and a CI job that needs a reachable control plane, a credential
// and a network path is a CI job that fails for reasons unrelated to the
// change it is reviewing. Compiling a Terraform plan is a pure function of
// the plan and the price book, both of which are already in the binary, so
// nothing is gained by making it a network call.
//
// Only the standard flag package is used, so a subcommand's flags are parsed
// by that subcommand's own FlagSet rather than by a framework's dispatcher.
// Human-readable output is the default; --json is available on every command
// for a machine consumer.
//
// Traceability: REQ-CLI-001, REQ-COMP-006, SPEC-OPS-003.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// command is one leaf subcommand.
type command struct {
	// Path is the full command words, e.g. {"cost", "test"}.
	Path []string
	// Summary is the one-line description shown by `coptx help`.
	Summary string
	// Run executes the command with the arguments following its path. It
	// returns the process exit code, so a command that gates CI (cost test)
	// can return 1 on a policy failure that is not a Go error.
	Run func(args []string) (exitCode int, err error)
}

func commands() []command {
	return []command{
		{[]string{"spec", "validate"}, "Validate a cloudoptix.yaml specification.", runSpecValidate},
		{[]string{"spec", "diff"}, "Show what changed between two specifications.", runSpecDiff},
		{[]string{"cost", "compile"}, "Price an infrastructure change set.", runCostCompile},
		{[]string{"cost", "test"}, "Run a cost regression suite against a change set; exits 1 on FAIL.", runCostTest},
		{[]string{"policy", "validate"}, "Validate a governance policy document.", runPolicyValidate},
		{[]string{"policy", "simulate"}, "Show how a policy would route the demo tenant's recommendations.", runPolicySimulate},
		{[]string{"demo", "seed"}, "Seed the demo tenant and print its summary.", runDemoSeed},
		{[]string{"demo", "run"}, "Seed the demo tenant and drive the full optimization flow.", runDemoRun},
		{[]string{"version"}, "Print version information.", runVersion},
	}
}

func main() {
	os.Exit(dispatch(os.Args[1:]))
}

func dispatch(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(os.Stdout)
		return 0
	}

	// Longest-path-first so "cost test" is matched before a hypothetical
	// bare "cost" ever could be. Matching shortest-first would make adding a
	// parent command silently shadow every child it has.
	cmds := commands()
	sort.SliceStable(cmds, func(i, j int) bool { return len(cmds[i].Path) > len(cmds[j].Path) })

	for _, c := range cmds {
		if matchesPath(args, c.Path) {
			code, err := c.Run(args[len(c.Path):])
			if err != nil {
				fmt.Fprintf(os.Stderr, "coptx %s: %v\n", strings.Join(c.Path, " "), err)
				if code == 0 {
					code = 1
				}
			}
			return code
		}
	}

	fmt.Fprintf(os.Stderr, "coptx: unknown command %q\n\n", strings.Join(args, " "))
	usage(os.Stderr)
	return 2
}

func matchesPath(args, path []string) bool {
	if len(args) < len(path) {
		return false
	}
	for i, p := range path {
		if args[i] != p {
			return false
		}
	}
	return true
}

func usage(w *os.File) {
	fmt.Fprintf(w, "coptx %s — the CloudOptix command-line tool\n\n", version)
	fmt.Fprintf(w, "Usage:\n  coptx <command> [flags]\n\nCommands:\n")
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-18s %s\n", strings.Join(c.Path, " "), c.Summary)
	}
	fmt.Fprintf(w, "\nEvery command accepts --json for machine-readable output.\n")
	fmt.Fprintf(w, "Run `coptx <command> -h` for that command's flags.\n")
}

func runVersion(args []string) (int, error) {
	fs := newFlagSet("version")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2, nil
	}
	info := map[string]string{"version": version, "commit": commit, "build_date": buildDate}
	if *asJSON {
		return 0, writeJSON(os.Stdout, info)
	}
	fmt.Printf("coptx %s (commit %s, built %s)\n", version, commit, buildDate)
	return 0, nil
}
