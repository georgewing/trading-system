package engine

import (
	"errors"
	"math"
	"strings"
)

var (
	ErrInvalidQuantity   = errors.New("invalid quantity")
	ErrQuantityPrecision = errors.New("quantity precision exceeded")
)

// ParseQuantity converts a decimal quantity string into integer lots using the
// supplied scale. The scale must be a positive power of ten, for example 100
// for two decimal places. Excess precision is rejected instead of rounded.
func ParseQuantity(s string, scale int64) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || scale <= 0 {
		return 0, ErrInvalidQuantity
	}
	if s[0] == '+' {
		s = s[1:]
	}
	if s == "" || s[0] == '-' {
		return 0, ErrInvalidQuantity
	}
	if strings.Count(s, ".") > 1 {
		return 0, ErrInvalidQuantity
	}

	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" && frac == "" {
		return 0, ErrInvalidQuantity
	}
	if whole == "" {
		whole = "0"
	}

	decimals, ok := decimalPlaces(scale)
	if !ok {
		return 0, ErrInvalidQuantity
	}
	if len(frac) > decimals {
		for i := decimals; i < len(frac); i++ {
			if frac[i] != '0' {
				return 0, ErrQuantityPrecision
			}
		}
		frac = frac[:decimals]
	}

	wholeVal, err := parseDigits(whole)
	if err != nil {
		return 0, err
	}
	if wholeVal > uint64(math.MaxInt64)/uint64(scale) {
		return 0, ErrInvalidQuantity
	}

	result := int64(wholeVal) * scale
	if frac == "" {
		return result, nil
	}

	fracVal, err := parseDigits(frac)
	if err != nil {
		return 0, err
	}
	for i := len(frac); i < decimals; i++ {
		if fracVal > uint64(math.MaxInt64)/10 {
			return 0, ErrInvalidQuantity
		}
		fracVal *= 10
	}
	if fracVal > uint64(math.MaxInt64-result) {
		return 0, ErrInvalidQuantity
	}

	return result + int64(fracVal), nil
}

func parseDigits(s string) (uint64, error) {
	if s == "" {
		return 0, ErrInvalidQuantity
	}
	var v uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, ErrInvalidQuantity
		}
		d := uint64(c - '0')
		if v > (math.MaxUint64-d)/10 {
			return 0, ErrInvalidQuantity
		}
		v = v*10 + d
	}
	return v, nil
}

func decimalPlaces(scale int64) (int, bool) {
	if scale <= 0 {
		return 0, false
	}
	n := 0
	for scale > 1 {
		if scale%10 != 0 {
			return 0, false
		}
		scale /= 10
		n++
	}
	return n, true
}
