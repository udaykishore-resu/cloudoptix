// Package contract_test asserts that the OpenAPI document and the code
// describe the same API.
//
// This is a contract test rather than a documentation lint. api/openapi.yaml
// is what a customer's client generator, a partner integration and the
// frontend's typed client are all built from; a route that exists in code
// but not in the document is a feature nobody can call, and a path in the
// document with no handler is a 404 a client was told to expect a 200 from.
// Both are silent failures — neither shows up in any other test — which is
// exactly the case a contract test exists for.
//
// Traceability: REQ-API-001, SPEC-API-001.
package contract_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
	transporthttp "github.com/udaykishore-resu/cloudoptix/internal/transport/http"
)

// specPath is resolved from the test's own directory so `go test ./...`
// works from anywhere in the repository.
const specPath = "../../api/openapi.yaml"

// apiPrefix is where router.go mounts every authenticated and public route.
// The OpenAPI document declares it in `servers`, so its paths are written
// without it.
const apiPrefix = "/api/v1"

// rootMountedPaths are served at the server root rather than under
// apiPrefix: the three health probes, the metrics scrape endpoint and the
// document itself. They are enumerated here rather than inferred because
// "everything not in BuildRoutes" would silently absorb a genuine omission.
var rootMountedPaths = map[string]bool{
	"/healthz":      true,
	"/readyz":       true,
	"/health":       true,
	"/metrics":      true,
	"/openapi.yaml": true,
}

type operation struct {
	Method string
	Path   string
}

func (o operation) String() string { return o.Method + " " + o.Path }

// TestOpenAPIMatchesRoutes asserts the two directions separately, because
// the two failures mean different things and are fixed in different files.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	documented := loadDocumentedOperations(t)
	implemented := loadImplementedOperations(t)

	require.NotEmpty(t, documented, "the OpenAPI document declared no operations at all")
	require.NotEmpty(t, implemented, "the route table declared no routes at all")

	t.Run("every implemented route is documented", func(t *testing.T) {
		var missing []string
		for _, op := range implemented {
			if !documented[op] {
				missing = append(missing, op.String())
			}
		}
		sort.Strings(missing)
		assert.Empty(t, missing,
			"%d route(s) exist in internal/transport/http/routes.go but not in api/openapi.yaml — "+
				"a client generated from the document cannot call them:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	})

	t.Run("every documented operation is implemented", func(t *testing.T) {
		implementedSet := map[operation]bool{}
		for _, op := range implemented {
			implementedSet[op] = true
		}
		for path := range rootMountedPaths {
			implementedSet[operation{Method: "GET", Path: path}] = true
		}

		var extra []string
		for op := range documented {
			if !implementedSet[op] {
				extra = append(extra, op.String())
			}
		}
		sort.Strings(extra)
		assert.Empty(t, extra,
			"%d operation(s) are documented in api/openapi.yaml with no handler behind them — "+
				"a client told to expect a 200 gets a 404:\n  %s",
			len(extra), strings.Join(extra, "\n  "))
	})

	t.Run("every route declares an operation the document can name", func(t *testing.T) {
		// routes.go carries a Name per route and openapi.yaml carries an
		// operationId per operation. They are not asserted equal here —
		// nothing enforces that today and pretending otherwise would be a
		// test of a convention rather than a contract — but a route with no
		// name at all cannot be referred to by either.
		for _, rt := range transporthttp.BuildRoutes(ports.Services{}) {
			assert.NotEmpty(t, rt.Name, "%s %s has no operation name", rt.Method, rt.Pattern)
		}
		for _, rt := range transporthttp.BuildPublicRoutes(ports.Services{}) {
			assert.NotEmpty(t, rt.Name, "%s %s has no operation name", rt.Method, rt.Pattern)
		}
	})
}

// openAPIDocument is the minimal shape this test reads. Decoding into a
// narrow struct rather than a generic map keeps the failure mode honest: a
// document whose `paths` key is missing or misspelled produces an empty
// result the assertions above catch, rather than a nil map index panic.
type openAPIDocument struct {
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

// httpMethods are the keys under a path item that name an operation.
// Everything else there (parameters, servers, summary, description, $ref) is
// metadata about the path, not an operation on it.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

func loadDocumentedOperations(t *testing.T) map[operation]bool {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(specPath))
	require.NoError(t, err, "api/openapi.yaml must exist and be readable")

	var doc openAPIDocument
	require.NoError(t, yaml.Unmarshal(raw, &doc), "api/openapi.yaml is not valid YAML")

	out := map[operation]bool{}
	for path, item := range doc.Paths {
		normalized := path
		if !rootMountedPaths[path] {
			normalized = apiPrefix + path
		}
		for key := range item {
			if !httpMethods[strings.ToLower(key)] {
				continue
			}
			out[operation{Method: strings.ToUpper(key), Path: normalizePath(normalized)}] = true
		}
	}
	return out
}

func loadImplementedOperations(t *testing.T) []operation {
	t.Helper()

	// BuildRoutes and BuildPublicRoutes take a ports.Services purely to
	// build their handler closures. A zero value is enough here: the test
	// reads only the method, pattern and name, and never serves a request —
	// which is what lets a contract test run without standing up the whole
	// application.
	var out []operation
	for _, rt := range transporthttp.BuildRoutes(ports.Services{}) {
		out = append(out, operation{Method: rt.Method, Path: normalizePath(apiPrefix + rt.Pattern)})
	}
	for _, rt := range transporthttp.BuildPublicRoutes(ports.Services{}) {
		out = append(out, operation{Method: rt.Method, Path: normalizePath(apiPrefix + rt.Pattern)})
	}
	return out
}

// pathParamRe matches both spellings of a path parameter: chi's
// {conversationID} and OpenAPI's {conversationId}, and any other casing.
var pathParamRe = regexp.MustCompile(`\{[^}]+\}`)

// normalizePath reduces a route pattern to its structural shape, replacing
// every path parameter with a positional placeholder.
//
// The parameter's NAME is deliberately discarded. chi and OpenAPI both name
// their parameters, but nothing requires the two to agree — routes.go writes
// {recommendationID} where the document may write {id} for the same
// segment — and a contract test that failed on that would be reporting a
// naming preference as a broken API. What must match is the shape: same
// method, same literal segments, same number of parameters in the same
// positions. A genuine mismatch (a segment added, removed or reordered)
// still fails.
func normalizePath(p string) string {
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		p = "/"
	}
	i := 0
	return pathParamRe.ReplaceAllStringFunc(p, func(string) string {
		i++
		return fmt.Sprintf("{%d}", i)
	})
}
