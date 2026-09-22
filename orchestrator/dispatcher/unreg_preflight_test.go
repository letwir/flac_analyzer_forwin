package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flac_analyzer/orchestrator/state"
)

type fakeUnregLookup struct {
	pingErr       error
	states        map[int]state.TaskState
	stateErr      map[int]error
	registrations map[unregTrackKey]struct{}
	snapshots     map[unregTrackKey]AnalysisSnapshot
	catalogErr    error
	requested     []unregTrackKey
	catalogCalls  int
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

func (f *fakeUnregLookup) Lookup(_ context.Context, requested []unregTrackKey) ([]analysisSnapshotRow, error) {
	f.catalogCalls++
	f.requested = append([]unregTrackKey(nil), requested...)
	if f.catalogErr != nil {
		return nil, f.catalogErr
	}
	var rows []analysisSnapshotRow
	for _, req := range requested {
		if f.snapshots != nil {
			if snap, ok := f.snapshots[req]; ok {
				rows = append(rows, analysisSnapshotRow{
					key:      req,
					snapshot: snap,
				})
				continue
			}
		}
		if _, ok := f.registrations[req]; ok {
			now := time.Now()
			rows = append(rows, analysisSnapshotRow{
				key: req,
				snapshot: AnalysisSnapshot{
					Exists:      true,
					RowVersion:  1,
					AnalyzedAt:  &now,
					Meta:        []byte(fmt.Sprintf(`{"analysis_schema_version": %d}`, AnalysisSchemaVersion)),
					Features:    []byte(`{"mix":{"feature":1},"demucs":{"vocals":{"f":1},"drums":{"f":1},"bass":{"f":1},"other":{"f":1},"guitar":{"f":1},"piano":{"f":1}}}`),
					Predictions: []byte(`{"pred":1}`),
				},
			})
		}
	}
	return rows, nil
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
			3: {Status: state.StatusRunning},
		},
		stateErr: map[int]error{},
		registrations: map[unregTrackKey]struct{}{
			mustUnregTrackKey(t, path, 1): {},
			mustUnregTrackKey(t, path, 2): {},
		},
	}
	result, err := filterUnregisteredSingleTasks(t.Context(), tasks, lookup)
	if err != nil {
		t.Fatal(err)
	}
	// Track 1: PG complete + SQLite completed -> PostgreSQLSkipped
	// Track 2: PG complete + SQLite absent -> Eligible (lightweight reconciliation)
	// Track 3: PG missing  + SQLite running -> SQLiteSkipped (active suppression)
	// Track 4: PG missing  + SQLite absent -> Eligible
	if result.SQLiteSkipped != 1 || result.PostgreSQLSkipped != 1 {
		t.Fatalf("unexpected skip counts: %+v", result)
	}
	if len(result.Eligible) != 2 || result.Eligible[0].TrackNumber != 2 || result.Eligible[1].TrackNumber != 4 {
		t.Fatalf("eligible=%+v, want tracks 2 and 4", result.Eligible)
	}
	if lookup.catalogCalls != 1 || len(lookup.requested) != len(tasks) {
		t.Fatalf("PostgreSQL batch calls=%d keys=%d, want 1 call with %d keys", lookup.catalogCalls, len(lookup.requested), len(tasks))
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

func TestNormalizedTrackNumberPolicy(t *testing.T) {
	for _, tt := range []struct {
		input   int
		want    int
		wantErr bool
	}{
		{input: 0, want: 1},
		{input: 1, want: 1},
		{input: 7, want: 7},
		{input: -1, wantErr: true},
	} {
		got, err := normalizedTrackNumber(tt.input)
		if (err != nil) != tt.wantErr || (!tt.wantErr && got != tt.want) {
			t.Fatalf("normalizedTrackNumber(%d)=(%d, %v), want (%d, error=%v)", tt.input, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestZeroTrackNumberUsesTrackOneRegistration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "single.flac")
	lookup := &fakeUnregLookup{
		states: map[int]state.TaskState{
			1: {Status: state.StatusCompleted},
		},
		stateErr: map[int]error{},
		registrations: map[unregTrackKey]struct{}{
			mustUnregTrackKey(t, path, 1): {},
		},
	}
	result, err := filterUnregisteredSingleTasks(t.Context(), []TaskPayload{{FlacPath: path, TrackNumber: 0}}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if result.PostgreSQLSkipped != 1 || len(result.Eligible) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(lookup.requested) != 1 || lookup.requested[0].trackNumber != 1 {
		t.Fatalf("requested keys=%+v, want Track 1", lookup.requested)
	}
}

func TestFilterUnregisteredSingleTasksScenarios(t *testing.T) {
	now := time.Now()
	completeSnapshot := AnalysisSnapshot{
		Exists:      true,
		RowVersion:  1,
		AnalyzedAt:  &now,
		Meta:        []byte(fmt.Sprintf(`{"analysis_schema_version": %d}`, AnalysisSchemaVersion)),
		Features:    []byte(`{"mix":{"feature":1},"demucs":{"vocals":{"f":1},"drums":{"f":1},"bass":{"f":1},"other":{"f":1},"guitar":{"f":1},"piano":{"f":1}}}`),
		Predictions: []byte(`{"pred":1}`),
	}
	incompleteSnapshot := AnalysisSnapshot{
		Exists:      true,
		RowVersion:  1,
		AnalyzedAt:  &now,
		Meta:        []byte(fmt.Sprintf(`{"analysis_schema_version": %d}`, AnalysisSchemaVersion)),
		Features:    []byte(`{"mix":{"feature":1},"demucs":{"vocals":{"f":1}}}`), // missing stems
		Predictions: []byte(`{"pred":1}`),
	}

	tests := []struct {
		name             string
		task             TaskPayload
		sqliteState      *state.TaskState  // nil means sql.ErrNoRows (absent)
		pgSnapshot       *AnalysisSnapshot // nil means not in pg
		wantEligible     bool
		wantSQLiteSkip   int
		wantPostgresSkip int
	}{
		{
			name:             "pg complete+sqlite completed skip",
			task:             TaskPayload{TrackNumber: 1},
			sqliteState:      &state.TaskState{Status: state.StatusCompleted},
			pgSnapshot:       &completeSnapshot,
			wantEligible:     false,
			wantSQLiteSkip:   0,
			wantPostgresSkip: 1,
		},
		{
			name:             "pg complete+sqlite missing eligible",
			task:             TaskPayload{TrackNumber: 1},
			sqliteState:      nil,
			pgSnapshot:       &completeSnapshot,
			wantEligible:     true,
			wantSQLiteSkip:   0,
			wantPostgresSkip: 0,
		},
		{
			name:             "pg missing+sqlite completed eligible",
			task:             TaskPayload{TrackNumber: 1},
			sqliteState:      &state.TaskState{Status: state.StatusCompleted},
			pgSnapshot:       nil,
			wantEligible:     true,
			wantSQLiteSkip:   0,
			wantPostgresSkip: 0,
		},
		{
			name:             "pg incomplete+sqlite completed eligible",
			task:             TaskPayload{TrackNumber: 1},
			sqliteState:      &state.TaskState{Status: state.StatusCompleted},
			pgSnapshot:       &incompleteSnapshot,
			wantEligible:     true,
			wantSQLiteSkip:   0,
			wantPostgresSkip: 0,
		},
		{
			name:             "active suppression pending",
			task:             TaskPayload{TrackNumber: 1},
			sqliteState:      &state.TaskState{Status: state.StatusPending},
			pgSnapshot:       &completeSnapshot,
			wantEligible:     false,
			wantSQLiteSkip:   1,
			wantPostgresSkip: 0,
		},
		{
			name:             "active suppression queued",
			task:             TaskPayload{TrackNumber: 1},
			sqliteState:      &state.TaskState{Status: state.StatusQueued},
			pgSnapshot:       &completeSnapshot,
			wantEligible:     false,
			wantSQLiteSkip:   1,
			wantPostgresSkip: 0,
		},
		{
			name:             "active suppression running",
			task:             TaskPayload{TrackNumber: 1},
			sqliteState:      &state.TaskState{Status: state.StatusRunning},
			pgSnapshot:       nil,
			wantEligible:     false,
			wantSQLiteSkip:   1,
			wantPostgresSkip: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "track.flac")
			tt.task.FlacPath = path
			key := mustUnregTrackKey(t, path, tt.task.TrackNumber)

			states := map[int]state.TaskState{}
			if tt.sqliteState != nil {
				states[tt.task.TrackNumber] = *tt.sqliteState
			}
			snapshots := map[unregTrackKey]AnalysisSnapshot{}
			if tt.pgSnapshot != nil {
				snapshots[key] = *tt.pgSnapshot
			}

			lookup := &fakeUnregLookup{
				states:    states,
				stateErr:  map[int]error{},
				snapshots: snapshots,
			}

			res, err := filterUnregisteredSingleTasks(t.Context(), []TaskPayload{tt.task}, lookup)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.SQLiteSkipped != tt.wantSQLiteSkip {
				t.Errorf("SQLiteSkipped = %d, want %d", res.SQLiteSkipped, tt.wantSQLiteSkip)
			}
			if res.PostgreSQLSkipped != tt.wantPostgresSkip {
				t.Errorf("PostgreSQLSkipped = %d, want %d", res.PostgreSQLSkipped, tt.wantPostgresSkip)
			}
			if (len(res.Eligible) > 0) != tt.wantEligible {
				t.Errorf("len(Eligible) = %d, wantEligible = %v", len(res.Eligible), tt.wantEligible)
			}
		})
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
