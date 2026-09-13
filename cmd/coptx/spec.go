package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// loadSpecFile reads and parses a cloudoptix.yaml document.
//
// The apiVersion check is not a formality: spec.Spec's shape is what every
// engine downstream is configured by, and a document written against a
// different version could parse cleanly into fields that no longer mean what
// the author intended. Refusing it here, by name, is better than validating
// a misread document and reporting confident nonsense.
func loadSpecFile(path string) (spec.Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return spec.Spec{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var s spec.Spec
	if err := yaml.Unmarshal(data, &s); err != nil {
		return spec.Spec{}, fmt.Errorf("%s is not a valid specification document: %w", path, err)
	}
	if s.APIVersion != "" && s.APIVersion != spec.CurrentAPIVersion {
		return spec.Spec{}, fmt.Errorf("%s declares apiVersion %q; this build understands %s",
			path, s.APIVersion, spec.CurrentAPIVersion)
	}
	return s, nil
}

// specValidateOutput is the --json shape.
type specValidateOutput struct {
	File         string            `json:"file"`
	Valid        bool              `json:"valid"`
	Blocking     bool              `json:"blocking"`
	Issues       []issueJSON       `json:"issues"`
	Completeness spec.Completeness `json:"completeness"`
}

type issueJSON struct {
	Path     string `json:"path"`
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

func runSpecValidate(args []string) (int, error) {
	fs := newFlagSet("spec validate")
	asJSON := fs.Bool("json", false, "emit JSON")
	handled, err := parseFlags(fs, args)
	if handled {
		return exitFor(err), err
	}

	if fs.NArg() == 0 {
		return 2, fmt.Errorf("usage: coptx spec validate <file>")
	}
	path := fs.Arg(0)
	s, err := loadSpecFile(path)
	if err != nil {
		return 1, err
	}

	validation := s.Validate()
	completeness := s.AssessCompleteness()

	if *asJSON {
		out := specValidateOutput{
			File: path, Valid: !validation.HasBlocking(), Blocking: validation.HasBlocking(),
			Completeness: completeness,
		}
		for _, iss := range validation.Issues {
			out.Issues = append(out.Issues, issueJSON{
				Path: iss.Path, Code: iss.Code, Severity: string(iss.Severity),
				Message: iss.Message, Hint: iss.Hint,
			})
		}
		if err := writeJSON(os.Stdout, out); err != nil {
			return 1, err
		}
		return exitCodeForValidation(validation.HasBlocking()), nil
	}

	fmt.Printf("Specification: %s\n", path)
	fmt.Printf("Completeness:  %.0f%% (%d required field(s) still missing)\n\n",
		completeness.Score*100, len(completeness.BlockingGaps))
	blocking := printIssues(os.Stdout, "Validation", validation)
	return exitCodeForValidation(blocking), nil
}

// specDiffOutput is the --json shape.
type specDiffOutput struct {
	From     string        `json:"from"`
	To       string        `json:"to"`
	Changes  []spec.Change `json:"changes"`
	Material bool          `json:"material"`
}

func runSpecDiff(args []string) (int, error) {
	fs := newFlagSet("spec diff")
	asJSON := fs.Bool("json", false, "emit JSON")
	handled, err := parseFlags(fs, args)
	if handled {
		return exitFor(err), err
	}

	if fs.NArg() < 2 {
		return 2, fmt.Errorf("usage: coptx spec diff <before.yaml> <after.yaml>")
	}
	fromPath, toPath := fs.Arg(0), fs.Arg(1)

	before, err := loadSpecFile(fromPath)
	if err != nil {
		return 1, err
	}
	after, err := loadSpecFile(toPath)
	if err != nil {
		return 1, err
	}

	changes := spec.SortChanges(spec.Diff(before, after))
	material := spec.HasMaterialChanges(changes)

	if *asJSON {
		return 0, writeJSON(os.Stdout, specDiffOutput{
			From: fromPath, To: toPath, Changes: changes, Material: material,
		})
	}

	if len(changes) == 0 {
		fmt.Printf("%s and %s are identical.\n", fromPath, toPath)
		return 0, nil
	}
	fmt.Printf("%s -> %s: %d change(s)\n\n", fromPath, toPath, len(changes))
	tw := newTable(os.Stdout)
	fmt.Fprintln(tw, "  SEVERITY\tCHANGE\tIMPACT")
	for _, c := range changes {
		impact := c.Impact
		if impact == "" {
			impact = "-"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", c.Severity, c.Summarize(), impact)
	}
	_ = tw.Flush()
	fmt.Println()
	if material {
		fmt.Println("This diff changes automation, governance or objectives — it needs a review, not a rubber stamp.")
	}
	return 0, nil
}

// exitCodeForValidation maps a blocking result to a non-zero exit so a CI
// step can gate on it without parsing output.
func exitCodeForValidation(blocking bool) int {
	if blocking {
		return 1
	}
	return 0
}

// exitFor picks the exit code for a flag-parse outcome: 0 for -h, 2 for a
// malformed flag (the conventional "usage error" code).
func exitFor(err error) int {
	if err == nil {
		return 0
	}
	return 2
}
