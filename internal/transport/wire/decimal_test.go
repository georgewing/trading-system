package wire

import "testing"

func TestDecimal(t *testing.T) {
	cases := []struct {
		value     int64
		precision int
		want      string
	}{
		{6251801000000, 8, "62518.01000000"},
		{100000000, 8, "1.00000000"},
		{1, 8, "0.00000001"},
		{0, 8, "0.00000000"},
		{-150000000, 8, "-1.50000000"},
		{42, 0, "42"},
		{42, -1, "42"},
		{-42, 0, "-42"},
	}

	for _, tc := range cases {
		if got := Decimal(tc.value, tc.precision); got != tc.want {
			t.Errorf("Decimal(%d, %d) = %q, want %q", tc.value, tc.precision, got, tc.want)
		}
	}
}

func TestDecimalHandlesMinInt64(t *testing.T) {
	// Negating MinInt64 must not overflow into a wrong sign or magnitude.
	const min = -1 << 63
	if got := Decimal(min, 0); got != "-9223372036854775808" {
		t.Fatalf("Decimal(MinInt64, 0) = %q", got)
	}
}

func TestLevel(t *testing.T) {
	got := Level(6251801000000, 150000000, 8, 8)
	if got[0] != "62518.01000000" || got[1] != "1.50000000" {
		t.Fatalf("Level() = %v", got)
	}
}
