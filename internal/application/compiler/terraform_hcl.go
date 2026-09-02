package compiler

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

// ParseTerraformHCL scans raw Terraform source (.tf files, concatenated) for
// top-level resource blocks and their scalar attributes, without a real HCL
// parser (hard-ruled out: no third-party dependency is available and the
// module proxy is blocked).
//
// What it understands:
//   - `resource "TYPE" "NAME" { ... }` blocks at any brace depth, tracked by
//     counting braces character-by-character so nested blocks never confuse
//     the scanner about where the resource block ends.
//   - Top-level (depth-1, directly inside the resource block) scalar
//     attribute assignments of the form `key = "string"`, `key = 123`,
//     `key = true`/`false`, each on its own line.
//   - `#` and `//` line comments, stripped before parsing a line (but not
//     inside a quoted string).
//
// What it deliberately does NOT understand, and reports as a warning rather
// than silently mispricing:
//   - Interpolation and expressions: `var.x`, `local.y`, `"${aws_vpc.main.id}"`,
//     function calls, ternaries, for-expressions. A value containing `${` or
//     starting with an identifier rather than a literal is skipped, not
//     evaluated or guessed.
//   - `count` and `for_each`: the scanner emits exactly one RawResource per
//     resource block regardless of a `count`/`for_each` meta-argument. When
//     either is present the resource is flagged with a warning so the
//     compiler can surface it as under-priced rather than silently treating
//     one declared block as one deployed resource.
//   - Nested blocks inside a resource (`root_block_device { ... }`,
//     `ebs_block_device { ... }`, `tag { ... }` in an ASG): their contents
//     are skipped, not descended into, so attributes that only exist inside a
//     nested block (root volume size/type/iops) are invisible to this parser.
//     A plan-JSON or state-based input is required to price those precisely.
//   - List and map attribute values, including `tags = { Key = "value" }`:
//     parseHCLLiteral recognizes only a quoted string, a bare number or a
//     bare true/false on the right-hand side, so a resource's tags are never
//     recovered from raw HCL and the untagged-resource CostRisk and the
//     require_tags regression check will not see them here — both need
//     Terraform plan JSON, which carries tags as an ordinary decoded map.
//   - Multi-line strings (heredocs `<<EOT ... EOT`) and multi-line lists —
//     any attribute value that does not fit on one line is skipped.
//   - Modules (`module "x" { source = ... }` blocks), `locals`, `variable`,
//     `data`, `provider` and `output` blocks: none produce a RawResource.
//   - Cross-resource references (e.g. resolving an ASG's launch_template to
//     the launch template's instance_type): each block is priced from its own
//     attributes only.
//
// A raw HCL file has no concept of before/after — it is a single desired
// state, not a diff — so every resource block is emitted as ChangeCreate.
// This is the honest reading for "how much would deploying this cost", which
// is what a raw-HCL input to the compiler is almost always used for (a module
// being evaluated before it is ever applied); it is not appropriate for
// pricing an update to already-deployed infrastructure, and Terraform plan
// JSON should be preferred whenever `terraform plan` can be run.
func ParseTerraformHCL(content []byte, fallbackRegion core.Region) ([]RawResource, []string, error) {
	blocks, warnings := scanHCLResourceBlocks(content)
	var out []RawResource
	for _, b := range blocks {
		attrs := Attrs{}
		for k, v := range b.attrs {
			attrs[k] = v
		}
		resourceWarnings := append([]string(nil), b.warnings...)
		if b.hasCount {
			resourceWarnings = append(resourceWarnings, fmt.Sprintf(
				"%s: has a count/for_each meta-argument the HCL scanner cannot expand; priced as a single instance — verify the actual replica count",
				b.address))
		}
		out = append(out, RawResource{
			Address:  b.address,
			Type:     b.resourceType,
			Action:   simulate.ChangeCreate,
			Region:   regionFromAttrs(attrs, fallbackRegion),
			After:    attrs,
			Tags:     attrs.Tags(),
			Warnings: resourceWarnings,
		})
	}
	return out, warnings, nil
}

type hclBlock struct {
	resourceType string
	name         string
	address      string
	attrs        map[string]any
	hasCount     bool
	warnings     []string
}

var (
	resourceHeaderPattern = regexp.MustCompile(`^resource\s+"([^"]+)"\s+"([^"]+)"\s*\{`)
	attrLinePattern       = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.+)$`)
)

// scanHCLResourceBlocks walks the source line by line, tracking brace depth.
// It only inspects attribute lines while depth is exactly 1 relative to a
// resource block's opening brace (i.e. a direct child of the resource, not
// inside a nested block), which is what makes nested blocks transparent to
// attribute extraction without needing to parse their contents.
func scanHCLResourceBlocks(content []byte) ([]hclBlock, []string) {
	var blocks []hclBlock
	var warnings []string

	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var cur *hclBlock
	depth := 0 // brace depth relative to the current resource block's opening line
	inHeredoc := false
	heredocTag := ""

	for scanner.Scan() {
		raw := scanner.Text()
		line := stripHCLComment(raw)
		trimmed := strings.TrimSpace(line)

		if inHeredoc {
			if strings.TrimSpace(raw) == heredocTag {
				inHeredoc = false
			}
			continue
		}

		if cur == nil {
			if m := resourceHeaderPattern.FindStringSubmatch(trimmed); m != nil {
				cur = &hclBlock{resourceType: m[1], name: m[2], attrs: map[string]any{}}
				cur.address = cur.resourceType + "." + cur.name
				depth = strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
				continue
			}
			continue
		}

		// Inside a resource block.
		opens := strings.Count(line, "{")
		closes := strings.Count(line, "}")

		if depth == 1 {
			if m := attrLinePattern.FindStringSubmatch(trimmed); m != nil && opens == 0 {
				key, valStr := m[1], strings.TrimSpace(m[2])
				if key == "count" || key == "for_each" {
					cur.hasCount = true
				} else if strings.HasPrefix(valStr, "<<") {
					inHeredoc = true
					heredocTag = strings.TrimPrefix(strings.TrimPrefix(valStr, "<<-"), "<<")
					heredocTag = strings.TrimSpace(heredocTag)
				} else if v, ok := parseHCLLiteral(valStr); ok {
					cur.attrs[key] = v
				} else {
					cur.warnings = append(cur.warnings, fmt.Sprintf(
						"attribute %q is an expression the HCL scanner cannot evaluate and was skipped", key))
				}
			}
		}

		depth += opens - closes
		if depth <= 0 {
			blocks = append(blocks, *cur)
			cur = nil
			depth = 0
		}
	}
	return blocks, warnings
}

func stripHCLComment(line string) string {
	inString := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '"' && (i == 0 || line[i-1] != '\\') {
			inString = !inString
		}
		if !inString {
			if c == '#' {
				return line[:i]
			}
			if c == '/' && i+1 < len(line) && line[i+1] == '/' {
				return line[:i]
			}
		}
	}
	return line
}

// parseHCLLiteral recognizes exactly a quoted string with no interpolation, a
// bare number, or a bare true/false. Anything else — a reference
// (var.x, aws_vpc.main.id), an interpolated string ("${...}"), a list, a map,
// a function call — returns (nil, false), which the caller reports as a skip
// rather than a guess.
func parseHCLLiteral(s string) (any, bool) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		inner := s[1 : len(s)-1]
		if strings.Contains(inner, "${") {
			return nil, false
		}
		return inner, true
	}
	if s == "true" {
		return true, true
	}
	if s == "false" {
		return false, true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, true
	}
	return nil, false
}
