// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package play

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/api/androidpublisher/v3"
)

const nanosPerUnit = 1_000_000_000

// ParseDecimal converts a decimal string such as "4.99" into whole units and
// nanos (billionths of a unit), the form the API's Money type uses. The
// conversion is exact: it never goes through a float. At most nine fractional
// digits are accepted, and for a negative amount both results are negative or
// zero.
func ParseDecimal(s string) (units int64, nanos int64, err error) {
	rest := s
	negative := false
	if strings.HasPrefix(rest, "-") {
		negative = true
		rest = rest[1:]
	}

	whole, fraction, hasPoint := strings.Cut(rest, ".")
	if whole == "" || !allDigits(whole) || (hasPoint && (fraction == "" || !allDigits(fraction))) {
		return 0, 0, fmt.Errorf("%q is not a decimal number such as \"4.99\"", s)
	}
	if len(fraction) > 9 {
		return 0, 0, fmt.Errorf("%q has more than 9 fractional digits", s)
	}

	units, err = strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("%q is out of range", s)
	}

	if fraction != "" {
		nanos, err = strconv.ParseInt(fraction+strings.Repeat("0", 9-len(fraction)), 10, 64)
		if err != nil {
			return 0, 0, errors.New("invalid fractional part")
		}
	}

	if negative {
		units, nanos = -units, -nanos
	}

	return units, nanos, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

// FormatDecimal is the inverse of ParseDecimal. It writes the shortest decimal
// string for the amount: no trailing fractional zeros and no decimal point for
// a whole number.
func FormatDecimal(units, nanos int64) string {
	// Normalize a nanos value outside (-1e9, 1e9), which the API does not send
	// but which would otherwise print wrongly.
	units += nanos / nanosPerUnit
	nanos %= nanosPerUnit

	// Mixed signs (1 unit, -250000000 nanos) are likewise not valid Money, but
	// folding them keeps the output a true rendering of the value.
	if units > 0 && nanos < 0 {
		units--
		nanos += nanosPerUnit
	} else if units < 0 && nanos > 0 {
		units++
		nanos -= nanosPerUnit
	}

	negative := units < 0 || nanos < 0
	if negative {
		units, nanos = -units, -nanos
	}

	out := strconv.FormatInt(units, 10)
	if nanos != 0 {
		out += "." + strings.TrimRight(fmt.Sprintf("%09d", nanos), "0")
	}
	if negative {
		out = "-" + out
	}

	return out
}

// DecimalsEqual reports whether two decimal strings denote the same amount,
// so that "4.50" equals "4.5". Strings that do not parse are never equal.
func DecimalsEqual(a, b string) bool {
	aUnits, aNanos, err := ParseDecimal(a)
	if err != nil {
		return false
	}
	bUnits, bNanos, err := ParseDecimal(b)
	if err != nil {
		return false
	}

	return aUnits == bUnits && aNanos == bNanos
}

// NewMoney builds the API's Money from a currency code and a decimal string.
func NewMoney(currencyCode, amount string) (*androidpublisher.Money, error) {
	units, nanos, err := ParseDecimal(amount)
	if err != nil {
		return nil, err
	}

	return &androidpublisher.Money{CurrencyCode: currencyCode, Units: units, Nanos: nanos}, nil
}

// MoneyAmount renders the amount of an API Money as a decimal string.
func MoneyAmount(m *androidpublisher.Money) string {
	if m == nil {
		return ""
	}

	return FormatDecimal(m.Units, m.Nanos)
}
