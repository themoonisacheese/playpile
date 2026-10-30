package money

import "testing"

func TestFormat(t *testing.T) {
	cases := []struct {
		minor    int64
		currency string
		want     string
	}{
		{1999, "USD", "$19.99"},
		{19900, "PLN", "199,00\u00a0z\u0142"},
		{4999, "EUR", "\u20ac49.99"},
		{123456, "USD", "$1,234.56"},
		{123456789, "USD", "$1,234,567.89"},
		{123456, "PLN", "1\u00a0234,56\u00a0z\u0142"},
		{9800, "JPY", "\u00a59,800"},
		{0, "USD", "$0.00"},
		{5, "USD", "$0.05"},
		{100, "USD", "$1.00"},
		{-1999, "USD", "-$19.99"},
		{4999, "XYZ", "XYZ 49.99"},
		{4999, "", "49.99"},
	}
	for _, c := range cases {
		if got := Format(c.minor, c.currency); got != c.want {
			t.Errorf("Format(%d, %q) = %q, want %q", c.minor, c.currency, got, c.want)
		}
	}
}

func TestDigits(t *testing.T) {
	if Digits("JPY") != 0 || Digits("USD") != 2 || Digits("nonsense") != 2 {
		t.Fatal("unexpected decimal digits")
	}
}
