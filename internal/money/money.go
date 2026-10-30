// Package money formats Steam's integer minor-unit prices for display.
package money

import (
	"strconv"
	"strings"
)

type format struct {
	symbol string
	before bool // symbol printed before the number ("$19.99" vs "19,99 zł")
	digits int  // decimal places
}

// Steam uses two decimals for most currencies and none for a few.
var formats = map[string]format{
	"USD": {symbol: "$", before: true, digits: 2},
	"CAD": {symbol: "C$", before: true, digits: 2},
	"AUD": {symbol: "A$", before: true, digits: 2},
	"NZD": {symbol: "NZ$", before: true, digits: 2},
	"MXN": {symbol: "MX$", before: true, digits: 2},
	"BRL": {symbol: "R$", before: true, digits: 2},
	"EUR": {symbol: "\u20ac", before: true, digits: 2},
	"GBP": {symbol: "\u00a3", before: true, digits: 2},
	"JPY": {symbol: "\u00a5", before: true, digits: 0},
	"CNY": {symbol: "\u00a5", before: true, digits: 2},
	"INR": {symbol: "\u20b9", before: true, digits: 2},
	"KRW": {symbol: "\u20a9", before: true, digits: 0},
	"PLN": {symbol: "z\u0142", before: false, digits: 2},
	"CZK": {symbol: "K\u010d", before: false, digits: 2},
	"RUB": {symbol: "\u20bd", before: false, digits: 2},
	"UAH": {symbol: "\u20b4", before: false, digits: 2},
	"TRY": {symbol: "\u20ba", before: false, digits: 2},
	"SEK": {symbol: "kr", before: false, digits: 2},
	"NOK": {symbol: "kr", before: false, digits: 2},
	"DKK": {symbol: "kr", before: false, digits: 2},
	"HUF": {symbol: "Ft", before: false, digits: 0},
	"CHF": {symbol: "CHF", before: true, digits: 2},
	"ILS": {symbol: "\u20aa", before: true, digits: 2},
	"SAR": {symbol: "SR", before: true, digits: 2},
	"AED": {symbol: "AED", before: true, digits: 2},
	"ZAR": {symbol: "R", before: true, digits: 2},
	"SGD": {symbol: "S$", before: true, digits: 2},
	"HKD": {symbol: "HK$", before: true, digits: 2},
	"TWD": {symbol: "NT$", before: true, digits: 0},
	"CLP": {symbol: "CLP$", before: true, digits: 0},
	"COP": {symbol: "COL$", before: true, digits: 0},
	"PEN": {symbol: "S/", before: true, digits: 2},
	"IDR": {symbol: "Rp", before: true, digits: 0},
	"MYR": {symbol: "RM", before: true, digits: 2},
	"PHP": {symbol: "\u20b1", before: true, digits: 2},
	"THB": {symbol: "\u0e3f", before: true, digits: 2},
	"VND": {symbol: "\u20ab", before: false, digits: 0},
}

// Digits reports the number of decimal places Steam uses for a currency.
func Digits(currency string) int {
	if f, ok := formats[strings.ToUpper(currency)]; ok {
		return f.digits
	}
	return 2
}

// Format renders minor units (Steam's integer cents) in the given currency,
// e.g. Format(4999, "PLN") == "49,99 zł". Unknown currencies fall back to
// "CODE 49.99" so nothing ever renders as a bare number.
func Format(minor int64, currency string) string {
	code := strings.ToUpper(strings.TrimSpace(currency))
	f, known := formats[code]
	if !known {
		f = format{digits: 2}
	}

	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}

	// Currencies that trail their symbol (zł, Kč, kr, ₽) are written the
	// European way: 1 234,56 rather than 1,234.56.
	groupSep, decSep := ",", "."
	if known && !f.before {
		groupSep, decSep = "\u00a0", ","
	}

	num := formatNumber(minor, f.digits, groupSep, decSep)

	switch {
	case !known:
		if code == "" {
			return sign + num
		}
		return sign + code + " " + num
	case f.before:
		return sign + f.symbol + num
	default:
		return sign + num + "\u00a0" + f.symbol
	}
}

// formatNumber renders minor units with thousands separators: 123456 -> "1,234.56".
func formatNumber(minor int64, digits int, groupSep, decSep string) string {
	if digits <= 0 {
		return group(strconv.FormatInt(minor, 10), groupSep)
	}
	div := int64(1)
	for i := 0; i < digits; i++ {
		div *= 10
	}
	whole := minor / div
	frac := minor % div
	fracStr := strconv.FormatInt(frac, 10)
	if pad := digits - len(fracStr); pad > 0 {
		fracStr = strings.Repeat("0", pad) + fracStr
	}
	return group(strconv.FormatInt(whole, 10), groupSep) + decSep + fracStr
}

func group(digits, sep string) string {
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	pre := len(digits) % 3
	if pre > 0 {
		b.WriteString(digits[:pre])
	}
	for i := pre; i < len(digits); i += 3 {
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}
