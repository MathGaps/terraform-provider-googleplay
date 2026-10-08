// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package play

import (
	"encoding/json"
	"fmt"
	"testing"

	"google.golang.org/api/androidpublisher/v3"
)

func TestParseDecimal(t *testing.T) {
	tests := []struct {
		in    string
		units int64
		nanos int64
	}{
		{"0", 0, 0},
		{"5", 5, 0},
		{"4.99", 4, 990_000_000},
		{"4.50", 4, 500_000_000},
		{"4.5", 4, 500_000_000},
		{"0.99", 0, 990_000_000},
		{"0.01", 0, 10_000_000},
		{"0.000000001", 0, 1},
		{"1234567.123456789", 1234567, 123_456_789},
		{"500", 500, 0},
		{"007.10", 7, 100_000_000},
		{"-1.75", -1, -750_000_000},
		{"-0.5", 0, -500_000_000},
		// The classic binary floating-point casualties.
		{"0.1", 0, 100_000_000},
		{"0.3", 0, 300_000_000},
		{"1.1", 1, 100_000_000},
		{"19.99", 19, 990_000_000},
		{"9223372036854775807.999999999", 9223372036854775807, 999_999_999},
	}

	for _, tt := range tests {
		units, nanos, err := ParseDecimal(tt.in)
		if err != nil {
			t.Errorf("ParseDecimal(%q) returned an error: %v", tt.in, err)

			continue
		}
		if units != tt.units || nanos != tt.nanos {
			t.Errorf("ParseDecimal(%q) = %d units, %d nanos; want %d, %d", tt.in, units, nanos, tt.units, tt.nanos)
		}
	}
}

func TestParseDecimalRejects(t *testing.T) {
	for _, in := range []string{
		"", ".", "4.", ".99", "4,99", "4.99 ", " 4.99", "$4.99", "4.99USD", "1e3", "0x10", "+1", "--1", "1.2.3",
		"1.0000000001",        // ten fractional digits
		"9223372036854775808", // one past the largest int64
		"NaN", "Inf", "4.9٩",  // not ASCII digits
	} {
		if units, nanos, err := ParseDecimal(in); err == nil {
			t.Errorf("ParseDecimal(%q) = %d, %d; want an error", in, units, nanos)
		}
	}
}

func TestFormatDecimal(t *testing.T) {
	tests := []struct {
		units int64
		nanos int64
		want  string
	}{
		{0, 0, "0"},
		{5, 0, "5"},
		{4, 990_000_000, "4.99"},
		{4, 500_000_000, "4.5"},
		{0, 990_000_000, "0.99"},
		{0, 1, "0.000000001"},
		{0, 10_000_000, "0.01"},
		{-1, -750_000_000, "-1.75"},
		{0, -500_000_000, "-0.5"},
		{1234567, 123_456_789, "1234567.123456789"},
		// Not valid Money, but printed as the value they denote.
		{1, 1_500_000_000, "2.5"},
		{2, -250_000_000, "1.75"},
		{-2, 250_000_000, "-1.75"},
	}

	for _, tt := range tests {
		if got := FormatDecimal(tt.units, tt.nanos); got != tt.want {
			t.Errorf("FormatDecimal(%d, %d) = %q, want %q", tt.units, tt.nanos, got, tt.want)
		}
	}
}

// Every amount with up to three decimals survives a round trip unchanged,
// which no float64 path can promise.
func TestDecimalRoundTrip(t *testing.T) {
	for units := int64(0); units < 120; units++ {
		for thousandths := int64(0); thousandths < 1000; thousandths++ {
			in := fmt.Sprintf("%d.%03d", units, thousandths)

			gotUnits, gotNanos, err := ParseDecimal(in)
			if err != nil {
				t.Fatalf("ParseDecimal(%q): %v", in, err)
			}
			if gotUnits != units || gotNanos != thousandths*1_000_000 {
				t.Fatalf("ParseDecimal(%q) = %d, %d", in, gotUnits, gotNanos)
			}

			out := FormatDecimal(gotUnits, gotNanos)
			if !DecimalsEqual(in, out) {
				t.Fatalf("%q formatted as %q, a different amount", in, out)
			}

			backUnits, backNanos, err := ParseDecimal(out)
			if err != nil || backUnits != units || backNanos != gotNanos {
				t.Fatalf("%q -> %q -> %d, %d (%v)", in, out, backUnits, backNanos, err)
			}
		}
	}
}

func TestDecimalsEqual(t *testing.T) {
	equal := [][2]string{{"4.50", "4.5"}, {"5", "5.000"}, {"0.99", "00.990"}, {"-0", "0"}}
	for _, pair := range equal {
		if !DecimalsEqual(pair[0], pair[1]) {
			t.Errorf("DecimalsEqual(%q, %q) = false", pair[0], pair[1])
		}
	}

	different := [][2]string{{"4.50", "4.51"}, {"5", "50"}, {"1", "-1"}, {"x", "x"}, {"", ""}}
	for _, pair := range different {
		if DecimalsEqual(pair[0], pair[1]) {
			t.Errorf("DecimalsEqual(%q, %q) = true", pair[0], pair[1])
		}
	}
}

// The API encodes units as a JSON string and nanos as a number; both must
// survive the generated client's encoding.
func TestMoneyJSONRoundTrip(t *testing.T) {
	for _, amount := range []string{"4.99", "0.99", "500", "1234567.01", "0"} {
		money, err := NewMoney("USD", amount)
		if err != nil {
			t.Fatalf("NewMoney(%q): %v", amount, err)
		}

		encoded, err := json.Marshal(money)
		if err != nil {
			t.Fatal(err)
		}

		var decoded androidpublisher.Money
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("decoding %s: %v", encoded, err)
		}

		if got := MoneyAmount(&decoded); got != amount {
			t.Errorf("%q came back as %q through %s", amount, got, encoded)
		}
	}

	if got := MoneyAmount(nil); got != "" {
		t.Errorf("MoneyAmount(nil) = %q", got)
	}
	if _, err := NewMoney("USD", "4,99"); err == nil {
		t.Error("NewMoney accepted a malformed amount")
	}
}
