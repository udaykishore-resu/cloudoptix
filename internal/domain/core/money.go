// Package core holds the shared value objects of the CloudOptix domain.
//
// Nothing in this package performs I/O. Every other domain package, every
// application service and every adapter is allowed to depend on core; core
// depends on nothing but the standard library. This is the innermost ring of
// the Clean Architecture layering enforced by tools/depguard.
//
// Traceability: SPEC-ARCH-002 (layering), SPEC-COST-001 (monetary precision).
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Money is an exact monetary amount held as integer micro-units of the
// currency (1 USD == 1_000_000 micros).
//
// Cloud economics multiplies tiny unit prices ($0.0000166667 per GB-second)
// by very large usage quantities. Float accumulation over millions of line
// items drifts enough to move a cost-per-transaction figure in the third
// decimal, which is exactly the digit CloudOptix reports on. So money is
// integer everywhere and only ever converted to float at a presentation
// boundary.
type Money struct {
	micros   int64
	currency Currency
}

// Currency is an ISO-4217 alphabetic code. AWS bills in a single currency per
// payer account, but CloudOptix is multi-tenant so the code travels with the
// amount rather than being a global constant.
type Currency string

const (
	USD Currency = "USD"
	EUR Currency = "EUR"
	GBP Currency = "GBP"
	INR Currency = "INR"
)

// MicrosPerUnit is the scaling factor between a Money's internal
// representation and one whole unit of currency.
const MicrosPerUnit int64 = 1_000_000

// ErrCurrencyMismatch is returned when two amounts in different currencies are
// combined. CloudOptix never silently converts; a mismatch is a modelling bug.
var ErrCurrencyMismatch = errors.New("core: currency mismatch")

// NewMoney builds an amount from whole and fractional units.
func NewMoney(units float64, ccy Currency) Money {
	if ccy == "" {
		ccy = USD
	}
	return Money{micros: int64(math.Round(units * float64(MicrosPerUnit))), currency: ccy}
}

// USDollars is the shorthand constructor used across the cost engines.
func USDollars(units float64) Money { return NewMoney(units, USD) }

// MoneyFromMicros builds an amount directly from micro-units.
func MoneyFromMicros(micros int64, ccy Currency) Money {
	if ccy == "" {
		ccy = USD
	}
	return Money{micros: micros, currency: ccy}
}

// ZeroUSD is the additive identity used to seed accumulations.
func ZeroUSD() Money { return Money{micros: 0, currency: USD} }

// Micros exposes the raw representation, for persistence only.
func (m Money) Micros() int64 { return m.micros }

// Currency reports the amount's currency, defaulting to USD for the zero value
// so that an uninitialised Money still participates in arithmetic.
func (m Money) Currency() Currency {
	if m.currency == "" {
		return USD
	}
	return m.currency
}

// Units renders the amount as a float. Use only at presentation boundaries.
func (m Money) Units() float64 { return float64(m.micros) / float64(MicrosPerUnit) }

// IsZero reports whether the amount is exactly zero.
func (m Money) IsZero() bool { return m.micros == 0 }

// IsNegative reports whether the amount is below zero. Negative money is legal
// in CloudOptix: it represents credits, refunds and savings deltas.
func (m Money) IsNegative() bool { return m.micros < 0 }

// Add returns m+other, erroring on a currency mismatch.
func (m Money) Add(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	return Money{micros: m.micros + other.micros, currency: m.Currency()}, nil
}

// MustAdd is Add for call sites that have already established a single
// currency, such as accumulating one account's line items.
func (m Money) MustAdd(other Money) Money {
	sum, err := m.Add(other)
	if err != nil {
		panic(err)
	}
	return sum
}

// Sub returns m-other.
func (m Money) Sub(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	return Money{micros: m.micros - other.micros, currency: m.Currency()}, nil
}

// MustSub is Sub for single-currency call sites.
func (m Money) MustSub(other Money) Money {
	d, err := m.Sub(other)
	if err != nil {
		panic(err)
	}
	return d
}

// Scale multiplies the amount by a dimensionless factor, rounding half away
// from zero at micro precision.
func (m Money) Scale(factor float64) Money {
	return Money{micros: int64(math.Round(float64(m.micros) * factor)), currency: m.Currency()}
}

// Div divides the amount by a count, returning zero for a zero divisor. This
// is the cost-per-transaction primitive.
func (m Money) Div(divisor float64) Money {
	if divisor == 0 {
		return Money{micros: 0, currency: m.Currency()}
	}
	return Money{micros: int64(math.Round(float64(m.micros) / divisor)), currency: m.Currency()}
}

// Ratio returns m/other as a dimensionless float, or 0 when other is zero.
func (m Money) Ratio(other Money) float64 {
	if other.micros == 0 {
		return 0
	}
	return float64(m.micros) / float64(other.micros)
}

// Cmp orders two amounts: -1, 0 or +1.
func (m Money) Cmp(other Money) int {
	switch {
	case m.micros < other.micros:
		return -1
	case m.micros > other.micros:
		return 1
	default:
		return 0
	}
}

// GreaterThan is sugar for Cmp(other) > 0.
func (m Money) GreaterThan(other Money) bool { return m.Cmp(other) > 0 }

// LessThan is sugar for Cmp(other) < 0.
func (m Money) LessThan(other Money) bool { return m.Cmp(other) < 0 }

// Abs returns the magnitude of the amount.
func (m Money) Abs() Money {
	if m.micros < 0 {
		return Money{micros: -m.micros, currency: m.Currency()}
	}
	return m
}

// Annualized projects a monthly amount over twelve months.
func (m Money) Annualized() Money { return m.Scale(12) }

// Monthly projects a daily amount over an average month.
func (m Money) Monthly() Money { return m.Scale(AverageDaysPerMonth) }

// AverageDaysPerMonth is 365.25/12, the figure used everywhere CloudOptix
// converts between daily, monthly and annual rates. Using a single constant
// keeps a recommendation's headline saving identical no matter which engine
// computed it.
const AverageDaysPerMonth = 30.4375

// HoursPerMonth is the standard AWS on-demand month used by the pricing book.
const HoursPerMonth = 730.0

func (m Money) compatible(other Money) error {
	if m.Currency() != other.Currency() {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.Currency(), other.Currency())
	}
	return nil
}

// String renders the amount for logs and CLI output.
func (m Money) String() string {
	sign := ""
	micros := m.micros
	if micros < 0 {
		sign = "-"
		micros = -micros
	}
	whole := micros / MicrosPerUnit
	frac := micros % MicrosPerUnit
	switch {
	case frac == 0:
		return fmt.Sprintf("%s%s %d", sign, m.Currency(), whole)
	case frac%10_000 == 0: // two decimal places is enough
		return fmt.Sprintf("%s%s %d.%02d", sign, m.Currency(), whole, frac/10_000)
	default:
		s := strings.TrimRight(fmt.Sprintf("%06d", frac), "0")
		return fmt.Sprintf("%s%s %d.%s", sign, m.Currency(), whole, s)
	}
}

// Format renders the amount the way the dashboard and copilot present it.
func (m Money) Format() string {
	symbol := "$"
	switch m.Currency() {
	case EUR:
		symbol = "€"
	case GBP:
		symbol = "£"
	case INR:
		symbol = "₹"
	}
	v := m.Units()
	neg := ""
	if v < 0 {
		neg, v = "-", -v
	}
	return neg + symbol + humanizeFloat(v)
}

func humanizeFloat(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	dot := strings.IndexByte(s, '.')
	intPart, frac := s[:dot], s[dot:]
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String() + frac
}

type moneyJSON struct {
	Micros   int64    `json:"micros"`
	Currency Currency `json:"currency"`
	Amount   float64  `json:"amount"`
	Display  string   `json:"display"`
}

// MarshalJSON emits both the exact representation and a display form, so the
// API is lossless for machines and readable for humans.
func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(moneyJSON{
		Micros:   m.micros,
		Currency: m.Currency(),
		Amount:   m.Units(),
		Display:  m.Format(),
	})
}

// UnmarshalJSON accepts either the structured form or a bare number (treated
// as whole USD units), which keeps hand-written fixtures readable.
func (m *Money) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "null" {
		*m = ZeroUSD()
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] != '{' {
		var f float64
		if err := json.Unmarshal(b, &f); err != nil {
			return err
		}
		*m = USDollars(f)
		return nil
	}
	var raw moneyJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw.Micros == 0 && raw.Amount != 0 {
		*m = NewMoney(raw.Amount, raw.Currency)
		return nil
	}
	*m = MoneyFromMicros(raw.Micros, raw.Currency)
	return nil
}

// MarshalText renders the amount as "<units> <CCY>", e.g. "5000.00 USD".
//
// Money implements encoding.TextMarshaler/TextUnmarshaler rather than the YAML
// interfaces so that the innermost domain package keeps a standard-library-only
// dependency set. gopkg.in/yaml.v3 honours the encoding.Text* interfaces for
// scalar nodes, which is what lets a Money appear in a policy document, a
// specification or a cost-regression suite. Without this, a YAML field typed
// as Money fails to parse at all — and because the failure aborts the whole
// document rather than the single field, one such field silently disables
// every rule in the file around it.
//
// JSON is unaffected: encoding/json prefers the json.Marshaler methods above,
// so the structured wire form stays lossless.
func (m Money) MarshalText() ([]byte, error) {
	return []byte(strconv.FormatFloat(m.Units(), 'f', -1, 64) + " " + string(m.Currency())), nil
}

// UnmarshalText accepts the forms a human would reasonably write in a
// configuration file: "5000", "5000.50", "$5,000", "USD 5000", "5000 USD".
// An unparseable value is an error rather than a silent zero, because a
// threshold that quietly becomes zero turns a guard into a wide-open gate.
func (m *Money) UnmarshalText(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" {
		*m = ZeroUSD()
		return nil
	}
	ccy := USD
	// A trailing or leading three-letter code names the currency.
	fields := strings.Fields(s)
	if len(fields) == 2 {
		if isCurrencyCode(fields[0]) {
			ccy, s = Currency(strings.ToUpper(fields[0])), fields[1]
		} else if isCurrencyCode(fields[1]) {
			ccy, s = Currency(strings.ToUpper(fields[1])), fields[0]
		}
	}
	s = strings.NewReplacer(",", "", "$", "", "€", "", "£", "", "₹", "", "_", "").Replace(s)
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return fmt.Errorf("core: %q is not a monetary amount: %w", string(b), err)
	}
	*m = NewMoney(v, ccy)
	return nil
}

func isCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			if r < 'a' || r > 'z' {
				return false
			}
		}
	}
	return true
}

// SumMoney adds a slice of amounts, returning zero USD for an empty slice.
func SumMoney(items []Money) (Money, error) {
	if len(items) == 0 {
		return ZeroUSD(), nil
	}
	total := Money{micros: 0, currency: items[0].Currency()}
	for _, it := range items {
		var err error
		if total, err = total.Add(it); err != nil {
			return Money{}, err
		}
	}
	return total, nil
}
