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

// CurrencyForCountry follows Steam's store-region currencies. Regions without
// a dedicated supported currency use USD. This also prices manual games in
// the same currency as the crew, rather than mixing currencies in the totals.
func CurrencyForCountry(country string) string {
	country = strings.ToUpper(strings.TrimSpace(country))
	switch country {
	case "AT", "BE", "CY", "DE", "EE", "ES", "FI", "FR", "GR", "HR", "IE", "IT", "LT", "LU", "LV", "MT", "NL", "PT", "SI", "SK", "BG", "CZ", "DK", "HU", "RO", "SE", "IS":
		return "EUR"
	case "GB":
		return "GBP"
	case "CA":
		return "CAD"
	case "AU":
		return "AUD"
	case "NZ":
		return "NZD"
	case "JP":
		return "JPY"
	case "KR":
		return "KRW"
	case "CN":
		return "CNY"
	case "IN":
		return "INR"
	case "BR":
		return "BRL"
	case "MX":
		return "MXN"
	case "PL":
		return "PLN"
	case "NO":
		return "NOK"
	case "CH", "LI":
		return "CHF"
	case "UA":
		return "UAH"
	case "RU":
		return "RUB"
	case "IL":
		return "ILS"
	case "SA":
		return "SAR"
	case "AE":
		return "AED"
	case "ZA":
		return "ZAR"
	case "SG":
		return "SGD"
	case "HK":
		return "HKD"
	case "TW":
		return "TWD"
	case "CL":
		return "CLP"
	case "CO":
		return "COP"
	case "PE":
		return "PEN"
	case "ID":
		return "IDR"
	case "MY":
		return "MYR"
	case "PH":
		return "PHP"
	case "TH":
		return "THB"
	case "VN":
		return "VND"
	case "KZ":
		return "KZT"
	case "KW":
		return "KWD"
	case "QA":
		return "QAR"
	case "CR":
		return "CRC"
	case "UY":
		return "UYU"
	default:
		return "USD"
	}
}

// Parse accepts a nonnegative decimal amount without floating-point rounding.
// A blank value means unknown, which is different from a zero/free price.
func Parse(amount, currency string) (*int64, error) {
	amount = strings.ReplaceAll(strings.TrimSpace(amount), ",", ".")
	if amount == "" {
		return nil, nil
	}
	digits := Digits(currency)
	parts := strings.Split(amount, ".")
	if len(parts) > 2 || parts[0] == "" {
		return nil, strconv.ErrSyntax
	}
	for _, part := range parts {
		for _, r := range part {
			if r < '0' || r > '9' {
				return nil, strconv.ErrSyntax
			}
		}
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
		if frac == "" || len(frac) > digits {
			return nil, strconv.ErrSyntax
		}
	}
	if len(parts[0]) > 12 {
		return nil, strconv.ErrRange
	}
	scale := int64(1)
	for i := 0; i < digits; i++ {
		scale *= 10
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, err
	}
	frac += strings.Repeat("0", digits-len(frac))
	var fraction int64
	if frac != "" {
		fraction, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return nil, err
		}
	}
	value := whole*scale + fraction
	if value > 1_000_000_000_000 {
		return nil, strconv.ErrRange
	}
	return &value, nil
}

// Decimal formats minor units for an editable decimal input (without symbols).
func Decimal(minor int64, currency string) string {
	digits := Digits(currency)
	scale := int64(1)
	for i := 0; i < digits; i++ {
		scale *= 10
	}
	if digits == 0 {
		return strconv.FormatInt(minor, 10)
	}
	frac := strconv.FormatInt(minor%scale, 10)
	return strconv.FormatInt(minor/scale, 10) + "." + strings.Repeat("0", digits-len(frac)) + frac
}
