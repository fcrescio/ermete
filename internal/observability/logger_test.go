package observability

import (
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  zapcore.Level
	}{
		{name: "debug plain", input: "debug", want: zapcore.DebugLevel},
		{name: "debug quoted", input: "\"debug\"", want: zapcore.DebugLevel},
		{name: "debug single quoted with spaces", input: "  'debug'  ", want: zapcore.DebugLevel},
		{name: "default info", input: "unknown", want: zapcore.InfoLevel},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseLevel(tc.input); got != tc.want {
				t.Fatalf("parseLevel(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
