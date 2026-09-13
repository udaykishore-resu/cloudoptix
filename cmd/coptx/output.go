package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// newFlagSet builds a subcommand FlagSet that reports its own errors and
// does not call os.Exit, so dispatch stays the single place a coptx process
// decides its exit code.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("coptx "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// parseFlags parses fs, translating -h into a clean exit rather than an
// error the caller would print as a failure.
func parseFlags(fs *flag.FlagSet, args []string) (handled bool, err error) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return true, err
	}
	return false, nil
}

// writeJSON emits v as indented JSON with a trailing newline. Indented, not
// compact: the primary consumer is a human reading CI logs, and `jq` does
// not care either way.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// newTable builds a tab-aligned writer. Flush must be called.
func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
}

// signedMoney renders a delta with an explicit sign, because "$1,200.00" and
// "-$1,200.00" mean opposite things in a cost table and a missing sign is
// the single most expensive formatting bug this tool could have.
func signedMoney(m core.Money) string {
	if m.IsNegative() {
		return "-" + m.Abs().Format()
	}
	if m.IsZero() {
		return m.Format()
	}
	return "+" + m.Format()
}

// issueLine renders one validation issue in the "severity path: message"
// shape both spec and policy validation print.
func issueLine(iss core.ValidationIssue) string {
	line := fmt.Sprintf("%-9s %s: %s", iss.Severity, iss.Path, iss.Message)
	if iss.Hint != "" {
		line += "\n            hint: " + iss.Hint
	}
	return line
}

// printIssues writes a validation result in human form and reports whether
// anything blocking was found.
func printIssues(w io.Writer, label string, v core.ValidationResult) bool {
	if len(v.Issues) == 0 {
		fmt.Fprintf(w, "%s: valid, no issues.\n", label)
		return false
	}
	fmt.Fprintf(w, "%s: %d issue(s)\n\n", label, len(v.Issues))
	for _, iss := range v.Issues {
		fmt.Fprintf(w, "  %s\n", issueLine(iss))
	}
	fmt.Fprintln(w)
	if v.HasBlocking() {
		fmt.Fprintf(w, "%s: BLOCKING — this document cannot be approved as written.\n", label)
		return true
	}
	fmt.Fprintf(w, "%s: no blocking issues.\n", label)
	return false
}
