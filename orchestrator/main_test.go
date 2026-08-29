package main

import "testing"

func TestValidateSingleModeOptions(t *testing.T) {
	tests := []struct {
		name      string
		single    string
		force     bool
		unreg     bool
		checkOnly bool
		wantErr   bool
	}{
		{"ordinary mode unchanged", "", false, false, false, false},
		{"single mode unchanged", "track.flac", false, false, false, false},
		{"unreg single", "track.flac", false, true, false, false},
		{"unreg check only", "track.flac", false, true, true, false},
		{"unreg missing single", "", false, true, false, true},
		{"unreg force conflict", "track.flac", true, true, false, true},
		{"check only missing unreg", "track.flac", false, false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSingleModeOptions(tt.single, tt.force, tt.unreg, tt.checkOnly)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}
