package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/vyrodovalexey/mcp-mock-server/internal/config"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// Output formats for `validate --output`.
const (
	outputText = "text"
	outputJSON = "json"
)

// validateFlags holds the parsed `validate` flags.
type validateFlags struct {
	// scenarioRoot bounds extends resolution (MOCK-703.7); empty means the
	// file's own directory.
	scenarioRoot string
	// output selects the report format: "text" (default) or "json".
	output string
}

// runValidate implements `mcpmock validate <file>` (MOCK-701.5). It composes and
// validates the scenario at the positional path and returns:
//
//	exitOK         (0) the scenario is valid,
//	exitValidation (1) the scenario is invalid (schema or semantic),
//	exitIO         (2) the file is missing/unreadable, or the invocation is bad.
//
// With --output json it emits, for a validation failure, a JSON object naming
// the source file and every problem's JSON Pointer and message. Diagnostics go
// to errOut; the machine-readable report goes to out.
func runValidate(out, errOut io.Writer, args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var vf validateFlags
	fs.StringVar(&vf.scenarioRoot, "scenario-root", "", "bound extends resolution to this directory")
	fs.StringVar(&vf.output, "output", outputText, "report format: text or json")
	fs.Usage = func() { validateUsage(errOut, fs) }

	if err := fs.Parse(args); err != nil {
		// flag already printed the error and usage; an unknown flag is a usage
		// (I/O) error, not a validation error (criterion 7).
		return exitIO
	}
	if vf.output != outputText && vf.output != outputJSON {
		fmt.Fprintf(errOut, "mcpmock validate: --output must be \"text\" or \"json\", got %q\n", vf.output)
		return exitIO
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(errOut, "mcpmock validate: exactly one scenario file is required")
		validateUsage(errOut, fs)
		return exitIO
	}
	return validateFile(out, errOut, rest[0], vf)
}

// validateFile composes and validates the scenario at path, rendering the
// outcome. It maps the loader's error taxonomy to the exit-code contract: a
// *config.ValidationError (schema/semantic) is exit 1; any other error (an I/O
// failure) is exit 2; success is exit 0.
func validateFile(out, errOut io.Writer, path string, vf validateFlags) int {
	doc, err := composeForValidate(path, vf.scenarioRoot)
	if err == nil {
		return reportValid(out, errOut, path, vf, doc.Kind)
	}

	var ve *config.ValidationError
	if errors.As(err, &ve) {
		return reportInvalid(out, errOut, ve, vf)
	}
	// Not a validation error ⇒ an I/O failure (missing/unreadable file). The
	// loader wraps these as plain os errors, distinct from ValidationError, so
	// the exit code is 2 (MOCK-701.5).
	fmt.Fprintf(errOut, "mcpmock validate: %v\n", err)
	return exitIO
}

// composeForValidate resolves the scenario root and composes the file exactly as
// NewFromFile would (post-composition validation, MOCK-701 / criterion: validate
// runs after composition). It returns the loader's native error so the caller
// can branch on *config.ValidationError.
func composeForValidate(path, root string) (scenario.Document, error) {
	sr, err := resolveScenarioRoot(root, path)
	if err != nil {
		return scenario.Document{}, err
	}
	return config.ComposeFile(sr, path)
}

// reportValid renders a successful validation and returns exitOK.
func reportValid(out, errOut io.Writer, path string, vf validateFlags, kind string) int {
	if vf.output == outputJSON {
		enc := json.NewEncoder(out)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(validateReport{OK: true, Source: path, Kind: kind})
		return exitOK
	}
	fmt.Fprintf(errOut, "mcpmock validate: %s is valid (kind %s)\n", path, kind)
	return exitOK
}

// reportInvalid renders a validation failure and returns exitValidation. In JSON
// mode it emits the source file and every problem's JSON Pointer and message
// (MOCK-701.5); in text mode it prints the loader's already-actionable message.
func reportInvalid(out, errOut io.Writer, ve *config.ValidationError, vf validateFlags) int {
	if vf.output == outputJSON {
		rep := validateReport{OK: false, Source: ve.Source}
		for _, p := range ve.Problems {
			rep.Problems = append(rep.Problems, problemReport{Pointer: p.Pointer, Message: p.Message})
		}
		enc := json.NewEncoder(out)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(rep)
		return exitValidation
	}
	fmt.Fprintf(errOut, "mcpmock validate: %v\n", ve)
	return exitValidation
}

// validateReport is the --output json document. It names the source file and,
// for a failure, every problem's JSON Pointer and message.
type validateReport struct {
	// OK reports whether the scenario validated.
	OK bool `json:"ok"`
	// Source is the file the document came from.
	Source string `json:"source"`
	// Kind is the document kind on success (e.g. "Scenario").
	Kind string `json:"kind,omitempty"`
	// Problems is the non-empty list of failures on a validation error.
	Problems []problemReport `json:"problems,omitempty"`
}

// problemReport is one validation problem in the JSON report: a JSON Pointer
// into the document and a human-readable message.
type problemReport struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// validateUsage prints the validate subcommand's help.
func validateUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, "Usage: mcpmock validate [flags] <scenario-file>")
	fmt.Fprintln(w, "\nValidate a scenario file (schema + semantics) without serving it.")
	fmt.Fprintln(w, "\nExit codes: 0 valid, 1 validation error, 2 I/O or usage error.")
	fmt.Fprintln(w, "\nFlags:")
	fs.PrintDefaults()
}
