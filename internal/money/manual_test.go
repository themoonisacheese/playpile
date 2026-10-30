package money

import "testing"

func TestManualPriceParsing(t *testing.T) {
	for _, tc := range []struct {
		amount, currency string
		want             int64
	}{{"29.99", "EUR", 2999}, {"0", "USD", 0}, {"1,5", "EUR", 150}, {"1500", "JPY", 1500}} {
		got, err := Parse(tc.amount, tc.currency)
		if err != nil || got == nil || *got != tc.want {
			t.Errorf("%+v => %v %v", tc, got, err)
		}
		if got != nil {
			round, err := Parse(Decimal(*got, tc.currency), tc.currency)
			if err != nil || round == nil || *round != *got {
				t.Errorf("roundtrip: %v %v", round, err)
			}
		}
	}
	if value, err := Parse("", "EUR"); err != nil || value != nil {
		t.Fatal("blank must be unknown")
	}
	for _, amount := range []string{"NaN", "-1", "1e2", "1.234", "1.2.3", "999999999999999", ".5"} {
		if _, err := Parse(amount, "EUR"); err == nil {
			t.Errorf("accepted %q", amount)
		}
	}
	if _, err := Parse("1.50", "JPY"); err == nil {
		t.Fatal("accepted fractional yen")
	}
	if CurrencyForCountry("fr") != "EUR" || CurrencyForCountry("US") != "USD" {
		t.Fatal("crew currency")
	}
}
