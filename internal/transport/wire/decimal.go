// Package wire converts domain values into the JSON representations shared by
// the HTTP and WebSocket transports.
//
// The domain carries prices and quantities as integers in minimal units. The
// wire carries decimal strings, because a browser cannot hold either an int64
// or a float without losing precision.
package wire

import (
	"strconv"
	"strings"
)

// Decimal renders value scaled by 10^precision as a decimal string.
//
// It never goes through float64: "62518.01" with precision 8 becomes
// "62518.01000000" by string manipulation only.
func Decimal(value int64, precision int) string {
	if precision <= 0 {
		return strconv.FormatInt(value, 10)
	}

	negative := value < 0
	var magnitude uint64
	if negative {
		// Negating math.MinInt64 would overflow.
		magnitude = uint64(-(value + 1)) + 1
	} else {
		magnitude = uint64(value)
	}

	digits := strconv.FormatUint(magnitude, 10)
	if len(digits) <= precision {
		digits = strings.Repeat("0", precision-len(digits)+1) + digits
	}

	head := digits[:len(digits)-precision]
	tail := digits[len(digits)-precision:]

	if negative {
		return "-" + head + "." + tail
	}
	return head + "." + tail
}

// Level renders one [price, quantity] pair for the wire.
func Level(price, qty int64, pricePrecision, qtyPrecision int) [2]string {
	return [2]string{
		Decimal(price, pricePrecision),
		Decimal(qty, qtyPrecision),
	}
}
