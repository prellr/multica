package handler

import (
	"reflect"
	"testing"
)

func TestMapMCPDirectoryTransports(t *testing.T) {
	got := mapMCPDirectoryTransports([]string{"streamable-http", "stdio", "sse", "streamable-http", "custom"})
	want := []string{"http", "stdio", "sse", "custom"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mapMCPDirectoryTransports() = %#v, want %#v", got, want)
	}
}

func TestParseBoundedInt(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "empty uses fallback", raw: "", want: 24},
		{name: "invalid uses fallback", raw: "nope", want: 24},
		{name: "below clamps to min", raw: "-1", want: 1},
		{name: "above clamps to max", raw: "1000", want: 100},
		{name: "valid passes through", raw: "42", want: 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseBoundedInt(tt.raw, 24, 1, 100); got != tt.want {
				t.Fatalf("parseBoundedInt(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}
