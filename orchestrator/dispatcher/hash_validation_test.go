package dispatcher

import "testing"

func TestIsHex32(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"d41d8cd98f00b204e9800998ecf8427e", true},   // valid MD5
		{"00000000000000000000000000000000", true},   // all zeros
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},   // all a's
		{"0123456789abcdef0123456789abcdef", true},   // mixed
		{"D41D8CD98F00B204E9800998ECF8427E", false},  // uppercase
		{"d41d8cd98f00b204e9800998ecf8427", false},   // 31 chars
		{"d41d8cd98f00b204e9800998ecf8427ee", false}, // 33 chars
		{"", false},
		{"not_a_valid_md5_hash_at_all!!!!", false},
		{"g41d8cd98f00b204e9800998ecf8427e", false}, // 'g' not hex
		{"d41d8cd98f00b204e9800998ecf8427 ", false}, // trailing space
	}
	for _, tt := range tests {
		got := isHex32(tt.input)
		if got != tt.want {
			t.Errorf("isHex32(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
