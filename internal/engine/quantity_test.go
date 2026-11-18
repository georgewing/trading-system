package engine

import (
	"errors"
	"testing"
)

func TestParseQuantity(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		scale   int64
		want    int64
		wantErr error
	}{
		{name: "integer", input: "1", scale: 100, want: 100},
		{name: "one decimal", input: "1.5", scale: 100, want: 150},
		{name: "trailing zero", input: "1.50", scale: 100, want: 150},
		{name: "leading dot", input: ".5", scale: 100, want: 50},
		{name: "smallest lot", input: "0.01", scale: 100, want: 1},
		{name: "zero", input: "0", scale: 100, want: 0},
		{name: "trailing dot", input: "1.", scale: 100, want: 100},
		{name: "plus sign", input: "+1.5", scale: 100, want: 150},
		{name: "integer scale", input: "1.0", scale: 1, want: 1},
		{name: "empty", input: "", scale: 100, wantErr: ErrInvalidQuantity},
		{name: "negative", input: "-1", scale: 100, wantErr: ErrInvalidQuantity},
		{name: "too much precision", input: "1.234", scale: 100, wantErr: ErrQuantityPrecision},
		{name: "integer scale rejects fraction", input: "1.1", scale: 1, wantErr: ErrQuantityPrecision},
		{name: "invalid character", input: "1x", scale: 100, wantErr: ErrInvalidQuantity},
		{name: "multiple dots", input: "1.2.3", scale: 100, wantErr: ErrInvalidQuantity},
		{name: "dot only", input: ".", scale: 100, wantErr: ErrInvalidQuantity},
		{name: "invalid scale", input: "1", scale: 8, wantErr: ErrInvalidQuantity},
		{name: "zero scale", input: "1", scale: 0, wantErr: ErrInvalidQuantity},
		{name: "overflow", input: "92233720368547758.08", scale: 100, wantErr: ErrInvalidQuantity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseQuantity(tt.input, tt.scale)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ParseQuantity(%q, %d) error = %v, want %v", tt.input, tt.scale, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseQuantity(%q, %d) error = %v", tt.input, tt.scale, err)
			}
			if got != tt.want {
				t.Fatalf("ParseQuantity(%q, %d) = %d, want %d", tt.input, tt.scale, got, tt.want)
			}
		})
	}
}
