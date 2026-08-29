package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"flac_analyzer/orchestrator/state"
)

type fakeUnregLookup struct {
	pingErr       error
	states        map[int]state.TaskState
	stateErr      map[int]error
	registrations map[unregTrackKey]struct{}
	catalogErr    error
}

func (f *fakeUnregLookup) Ping(context.Context) error {
	return f.pingErr
}

func (f *fakeUnregLookup) SQLiteTaskState(_ string, trackNumber int) (state.TaskState, error) {
	if err, ok := f.stateErr[trackNumber]; ok {
		return state.TaskState{}, err
	}
	if taskState, ok := f.states[trackNumber]; ok {
		return taskState, nil
	}
	return state.TaskState{}, sql.ErrNoRows
}

func (f *fakeUnregLookup) PostgreSQLRegistrations(context.Context) (map[unregTrackKey]struct{}, error) {
	return f.registrations, f.catalogErr
}

func TestClassifySQLiteUnregStatus(t *testing.T) {
	tests := []struct {
		status   state.TaskStatus
		decision sqliteUnregDecision
		wantErr  bool
	}{
		{state.StatusFailed, sqliteUnregEligible, false},
		{state.StatusFailedMaybeRetry, sqliteUnregEligible, false},
		{state.StatusPending, sqliteUnregSkip, false},
		{state.StatusQueued, sqliteUnregSkip, false},
		{state.StatusRunning, sqliteUnregSkip, false},
		{state.StatusCompleted, sqliteUnregSkip, false},
		{state.TaskStatus("UNKNOWN"), 0, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			got, err := classifySQLiteUnregStatus(tt.status)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.decision {
				t.Fatalf("decision=%v want=%v", got, tt.decision)
			}
		})
	}
}

func TestFilterUnregisteredSingleTasksFourQuadrants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "album.flac")
	tasks := []TaskPayload{
		{FlacPath: path, TrackNumber: 1},
		{FlacPath: path, TrackNumber: 2},
		{FlacPath: path, TrackNumber: 3},
		{FlacPath: path, TrackNumber: 4},
	}
	lookup := &fakeUnregLookup{
		states: map[int]state.TaskState{
			1: {Status: state.StatusCompleted},
			3: {Status: state.StatusFailed},
		},
		stateErr: map[int]error{},
		registrations: map[unregTrackKey]struct{}{
			mustUnregTrackKey(t, path, 2): {},
			mustUnregTrackKey(t, path, 3): {},
		},
	}
	result, err := filterUnregisteredSingleTasks(t.Context(), tasks, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if result.SQLiteSkipped != 1 || result.PostgreSQLSkipped != 2 {
		t.Fatalf("unexpected skip counts: %+v", result)
	}
	if len(result.Eligible) != 1 || result.Eligible[0].TrackNumber != 4 {
		t.Fatalf("eligible=%+v, want only track 4", result.Eligible)
	}
}

func TestFilterUnregisteredSingleTasksIsAllOrNothing(t *testing.T) {
	sentinel := errors.New("postgres://user:secret@example.invalid/db")
	lookup := &fakeUnregLookup{
		states:     map[int]state.TaskState{},
		stateErr:   map[int]error{},
		catalogErr: sentinel,
	}
	tasks := []TaskPayload{
		{FlacPath: filepath.Join(t.TempDir(), "album.flac"), TrackNumber: 1},
		{FlacPath: filepath.Join(t.TempDir(), "album.flac"), TrackNumber: 2},
	}
	result, err := filterUnregisteredSingleTasks(t.Context(), tasks, lookup)
	if err == nil {
		t.Fatal("expected PostgreSQL lookup failure")
	}
	if len(result.Eligible) != 0 {
		t.Fatalf("partial eligible result leaked: %+v", result.Eligible)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("error leaked connection details: %v", err)
	}
}

func TestFilterUnregisteredSingleTasksFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		lookup *fakeUnregLookup
	}{
		{
			name: "ping",
			lookup: &fakeUnregLookup{
				pingErr: errors.New("unavailable"),
			},
		},
		{
			name: "sqlite read",
			lookup: &fakeUnregLookup{
				states:   map[int]state.TaskState{},
				stateErr: map[int]error{1: errors.New("locked")},
			},
		},
		{
			name: "unknown status",
			lookup: &fakeUnregLookup{
				states: map[int]state.TaskState{1: {Status: state.TaskStatus("ALIEN")}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := filterUnregisteredSingleTasks(t.Context(), []TaskPayload{{FlacPath: filepath.Join(t.TempDir(), "x.flac"), TrackNumber: 1}}, tt.lookup)
			if err == nil || len(result.Eligible) != 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestRegistrationComparisonKey(t *testing.T) {
	base := t.TempDir()
	pathA := filepath.Join(base, "Artist", "..", "Album", "TRACK.FLAC")
	pathB := strings.ReplaceAll(filepath.Join(base, "Album", "track.flac"), `\`, "/")
	keyA, err := registrationComparisonKey(pathA)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := registrationComparisonKey(pathB)
	if err != nil {
		t.Fatal(err)
	}
	if keyA != keyB {
		t.Fatalf("normalized keys differ: %q != %q", keyA, keyB)
	}

	mapped, err := registrationComparisonKey(`N:\Music\Album\track.flac`)
	if err != nil {
		t.Fatal(err)
	}
	unc, err := registrationComparisonKey(`\\server\Music\Album\track.flac`)
	if err != nil {
		t.Fatal(err)
	}
	if mapped == unc {
		t.Fatal("mapped drive and UNC path must not be treated as aliases")
	}
}

func TestPostgreSQLStoredPathUsesSameCanonicalization(t *testing.T) {
	stored := `C:\Music\Album\..\Album\track.flac`
	input := `c:/music/album/TRACK.FLAC`
	storedKey, err := newUnregTrackKey(stored, 7)
	if err != nil {
		t.Fatal(err)
	}
	inputKey, err := newUnregTrackKey(input, 7)
	if err != nil {
		t.Fatal(err)
	}
	if storedKey != inputKey {
		t.Fatalf("stored and input keys differ: %+v != %+v", storedKey, inputKey)
	}
	otherTrack, err := newUnregTrackKey(input, 8)
	if err != nil {
		t.Fatal(err)
	}
	if storedKey == otherTrack {
		t.Fatal("track number must remain part of the registration key")
	}
}

func TestUnregRegistrationQueryContract(t *testing.T) {
	for _, required := range []string{
		"SELECT filepath, track_number",
		"FROM raw.library_flac",
		"analyzed_at IS NOT NULL",
	} {
		if !strings.Contains(unregRegistrationQuery, required) {
			t.Fatalf("query missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(unregRegistrationQuery), "history") {
		t.Fatal("query must not reference history tables")
	}
}

func mustUnregTrackKey(t *testing.T, path string, trackNumber int) unregTrackKey {
	t.Helper()
	key, err := newUnregTrackKey(path, trackNumber)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
