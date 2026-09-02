package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// TenantID identifies a CloudOptix tenant. It is a distinct type rather than a
// bare string on purpose: every repository method, every cache key and every
// event carries one, and the compiler is the cheapest place to catch a missing
// or swapped tenant scope.
//
// Traceability: REQ-SEC-003, SPEC-SEC-003 (tenant isolation).
type TenantID string

// String satisfies fmt.Stringer.
func (t TenantID) String() string { return string(t) }

// IsZero reports whether the tenant scope is unset.
func (t TenantID) IsZero() bool { return t == "" }

// Validate rejects tenant identifiers that could confuse a cache key or an
// object-storage prefix.
func (t TenantID) Validate() error {
	if t == "" {
		return fmt.Errorf("tenant id is required")
	}
	if !idPattern.MatchString(string(t)) {
		return fmt.Errorf("tenant id %q is not a valid identifier", string(t))
	}
	return nil
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{2,63}$`)

// ID is the generic surrogate identifier used by every aggregate that is not a
// tenant. It is a ULID-like sortable string: a millisecond timestamp prefix
// followed by random entropy, so identifiers order by creation time in an
// index without a separate column.
type ID string

// String satisfies fmt.Stringer.
func (i ID) String() string { return string(i) }

// IsZero reports whether the identifier is unset.
func (i ID) IsZero() bool { return i == "" }

const idAlphabet = "0123456789abcdefghjkmnpqrstvwxyz" // Crockford base32, no I/L/O/U

// NewID mints a sortable identifier with the given short prefix, for example
// NewID("rec") -> "rec_01j9x2m4qk_8f21c0de".
func NewID(prefix string) ID {
	ts := uint64(time.Now().UTC().UnixMilli())
	var enc [10]byte
	for i := 9; i >= 0; i-- {
		enc[i] = idAlphabet[ts&31]
		ts >>= 5
	}
	var entropy [4]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		// crypto/rand failing is unrecoverable; identifiers must be unique.
		panic(fmt.Sprintf("core: entropy source failed: %v", err))
	}
	return ID(fmt.Sprintf("%s_%s_%s", prefix, string(enc[:]), hex.EncodeToString(entropy[:])))
}

// Prefix returns the type prefix of an identifier, or "" when absent. It is
// used by the API layer to reject a well-formed identifier of the wrong kind.
func (i ID) Prefix() string {
	if idx := strings.IndexByte(string(i), '_'); idx > 0 {
		return string(i)[:idx]
	}
	return ""
}

// ARN is an AWS Amazon Resource Name. CloudOptix keeps it as a typed string
// and parses it lazily, because several services emit ARNs whose resource
// segment is service-specific.
type ARN string

// String satisfies fmt.Stringer.
func (a ARN) String() string { return string(a) }

// ParsedARN is the decomposed form of an ARN.
type ParsedARN struct {
	Partition string
	Service   string
	Region    string
	AccountID string
	Resource  string
	Type      string
}

// Parse decomposes the ARN. It tolerates the empty region and account
// segments used by global services such as S3 and IAM.
func (a ARN) Parse() (ParsedARN, bool) {
	parts := strings.SplitN(string(a), ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return ParsedARN{}, false
	}
	p := ParsedARN{
		Partition: parts[1],
		Service:   parts[2],
		Region:    parts[3],
		AccountID: parts[4],
		Resource:  parts[5],
	}
	if i := strings.IndexAny(p.Resource, "/:"); i > 0 {
		p.Type = p.Resource[:i]
		p.Resource = p.Resource[i+1:]
	}
	return p, true
}

// AccountID is a twelve-digit AWS account number.
type AccountID string

// String satisfies fmt.Stringer.
func (a AccountID) String() string { return string(a) }

var accountPattern = regexp.MustCompile(`^\d{12}$`)

// Validate checks the twelve-digit shape.
func (a AccountID) Validate() error {
	if !accountPattern.MatchString(string(a)) {
		return fmt.Errorf("aws account id %q must be 12 digits", string(a))
	}
	return nil
}

// Region is an AWS region code such as us-east-1.
type Region string

// String satisfies fmt.Stringer.
func (r Region) String() string { return string(r) }

// Environment classifies a deployment target. The production flag drives
// policy severity, approval requirements and blast-radius weighting, so it is
// a closed enumeration rather than a free string.
type Environment string

const (
	EnvProduction  Environment = "production"
	EnvStaging     Environment = "staging"
	EnvDevelopment Environment = "development"
	EnvTest        Environment = "test"
	EnvSandbox     Environment = "sandbox"
	EnvDR          Environment = "dr"
	EnvUnknown     Environment = "unknown"
)

// IsProduction reports whether changes here require the strict path.
func (e Environment) IsProduction() bool { return e == EnvProduction || e == EnvDR }

// String satisfies fmt.Stringer.
func (e Environment) String() string { return string(e) }

// NormalizeEnvironment maps the free-form values found in AWS tags onto the
// closed enumeration. Anything unrecognised becomes EnvUnknown rather than
// being guessed at, because a wrong production classification is the most
// expensive mistake the discovery engine can make.
func NormalizeEnvironment(raw string) Environment {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "prod", "production", "prd", "live":
		return EnvProduction
	case "stage", "staging", "stg", "preprod", "pre-prod", "uat":
		return EnvStaging
	case "dev", "development", "devel":
		return EnvDevelopment
	case "test", "qa", "testing":
		return EnvTest
	case "sandbox", "sbx", "playground":
		return EnvSandbox
	case "dr", "disaster-recovery", "failover":
		return EnvDR
	default:
		return EnvUnknown
	}
}

// Tags is the normalized tag map attached to every discovered resource. Keys
// are stored verbatim; lookups are case-insensitive because AWS tag keys are
// case-sensitive but humans are not.
type Tags map[string]string

// Get performs a case-insensitive lookup.
func (t Tags) Get(key string) (string, bool) {
	if t == nil {
		return "", false
	}
	if v, ok := t[key]; ok {
		return v, true
	}
	lower := strings.ToLower(key)
	for k, v := range t {
		if strings.ToLower(k) == lower {
			return v, true
		}
	}
	return "", false
}

// First returns the value of the first key present, trying each in order.
func (t Tags) First(keys ...string) string {
	for _, k := range keys {
		if v, ok := t.Get(k); ok && v != "" {
			return v
		}
	}
	return ""
}

// Clone returns an independent copy.
func (t Tags) Clone() Tags {
	if t == nil {
		return nil
	}
	out := make(Tags, len(t))
	for k, v := range t {
		out[k] = v
	}
	return out
}
