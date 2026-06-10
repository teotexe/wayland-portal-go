package pwrouter

import (
	"testing"
)

// TestEqualSlices tests the internal helper used to determine if
// application audio ports have changed between polling cycles.
func TestEqualSlices(t *testing.T) {
	tests := []struct {
		name     string
		a        []string
		b        []string
		expected bool
	}{
		{
			name:     "Both empty",
			a:        []string{},
			b:        []string{},
			expected: true,
		},
		{
			name:     "Identical slices",
			a:        []string{"12", "34"},
			b:        []string{"12", "34"},
			expected: true,
		},
		{
			name:     "Different lengths",
			a:        []string{"12"},
			b:        []string{"12", "34"},
			expected: false,
		},
		{
			name:     "Different contents",
			a:        []string{"12", "34"},
			b:        []string{"12", "99"},
			expected: false,
		},
		{
			name:     "Nil vs empty",
			a:        nil,
			b:        []string{},
			expected: true, // Go len() treats nil as 0, our func handles this safely
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := equalSlices(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("equalSlices(%v, %v) = %v; expected %v", tt.a, tt.b, result, tt.expected)
			}
		})
	}
}
