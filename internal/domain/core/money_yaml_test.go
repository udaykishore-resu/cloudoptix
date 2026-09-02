package core_test

import (
	"testing"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"gopkg.in/yaml.v3"
)

type holder struct {
	Amount core.Money `yaml:"amount"`
}

func TestMoneyYAMLRoundTrip(t *testing.T) {
	for _, in := range []string{"5000", "5000.50", "'$5,000'", "USD 5000", "'5000 USD'"} {
		var h holder
		if err := yaml.Unmarshal([]byte("amount: "+in+"\n"), &h); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if h.Amount.IsZero() {
			t.Fatalf("%s parsed to zero", in)
		}
		out, err := yaml.Marshal(h)
		if err != nil {
			t.Fatalf("marshal %s: %v", in, err)
		}
		var back holder
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Fatalf("round trip %s: %v", in, err)
		}
		if back.Amount.Micros() != h.Amount.Micros() {
			t.Fatalf("%s: round trip lost precision %d != %d", in, back.Amount.Micros(), h.Amount.Micros())
		}
	}
	var h holder
	if err := yaml.Unmarshal([]byte("amount: not-money\n"), &h); err == nil {
		t.Fatal("expected error for unparseable amount")
	}
}
