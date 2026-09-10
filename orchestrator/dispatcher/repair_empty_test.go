package dispatcher

import (
	"database/sql"
	"encoding/json"
	"testing"
)

func repairFixture(t *testing.T) *Dispatcher {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	// Execute the production guarded UPDATE against isolated rows. PostgreSQL
	// JSONB equality has the same result for these canonical JSON fixtures.
	for _, query := range []string{
		`ATTACH DATABASE ':memory:' AS raw`,
		`CREATE TABLE raw.library_flac (id INTEGER PRIMARY KEY, filepath TEXT, track_number INTEGER, audio_hash TEXT, meta TEXT, features TEXT, predictions TEXT, analyzed_at TEXT)`,
		`INSERT INTO raw.library_flac VALUES (1,'album.flac',1,'hash1','{}','{}','{}','old'),(2,'album.flac',2,'hash2','{"title":"healthy"}','{"mix":{"bpm":99}}','{}','old'),(3,'album.flac',3,'hash3','{"title":"keep"}','{}','{"mood":0.9}','old')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return &Dispatcher{pgDB: db}
}

func TestEmptyRepairSelectsOnlyMatchingTrack(t *testing.T) {
	d := repairFixture(t)
	tasks := []TaskPayload{{FlacPath: "album.flac", TrackNumber: 1}, {FlacPath: "album.flac", TrackNumber: 2}}
	selected, err := d.SelectEmptyJSONRepair(t.Context(), tasks, 1)
	if err != nil || len(selected) != 1 {
		t.Fatalf("selected=%v err=%v", selected, err)
	}
	if !selected[0].Force || selected[0].RepairAudioHash != "hash1" || selected[0].TrackNumber != 1 {
		t.Fatalf("wrong repair task: %+v", selected[0])
	}
	selected, err = d.SelectEmptyJSONRepair(t.Context(), tasks, 2)
	if err != nil || len(selected) != 0 {
		t.Fatalf("healthy record selected: %v %v", selected, err)
	}
	if _, err := d.SelectEmptyJSONRepair(t.Context(), tasks[1:], 1); err == nil {
		t.Fatal("missing CUE track accepted")
	}
	if _, err := d.SelectEmptyJSONRepair(t.Context(), tasks, 999); err == nil {
		t.Fatal("missing record accepted")
	}
}

func TestEmptyRepairGuardedPersistence(t *testing.T) {
	d := repairFixture(t)
	payload := IngestPayload{
		Task:      TaskPayload{FlacPath: "album.flac", TrackNumber: 1, RepairRecordID: 1, RepairAudioHash: "hash1", Title: "restored"},
		TrackHash: "hash1", LibrosaJSON: json.RawMessage(`{"mix":{"scalars":{"bpm":128}}}`),
		TensorJSON: json.RawMessage(`{}`), EssentiaJSON: json.RawMessage(`{"mood":0.8}`),
	}
	if result := d.UpsertTrackDirectly(t.Context(), payload); !result.Success || result.SavedToDLQ {
		t.Fatalf("repair failed: %+v", result)
	}
	var count int
	if err := d.pgDB.QueryRow(`SELECT count(*) FROM raw.library_flac`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("repair inserted a row: %d %v", count, err)
	}
	var features, meta, predictions string
	if err := d.pgDB.QueryRow(`SELECT features,meta,predictions FROM raw.library_flac WHERE id=1`).Scan(&features, &meta, &predictions); err != nil {
		t.Fatal(err)
	}
	if features != `{"mix":{"scalars":{"bpm":128}}}` || predictions != `{"mood":0.8}` {
		t.Fatalf("lost repair data: %s %s", features, predictions)
	}
	// A previously selected record that became healthy must not be overwritten.
	payload.LibrosaJSON = json.RawMessage(`{"mix":{"bpm":200}}`)
	if result := d.UpsertTrackDirectly(t.Context(), payload); result.Success || result.SavedToDLQ {
		t.Fatalf("healthy row overwritten or queued: %+v", result)
	}
	if err := d.pgDB.QueryRow(`SELECT features FROM raw.library_flac WHERE id=2`).Scan(&features); err != nil {
		t.Fatal(err)
	}
	if features != `{"mix":{"bpm":99}}` {
		t.Fatal("unselected healthy track changed")
	}
	payload.Task.RepairRecordID, payload.Task.TrackNumber = 3, 3
	payload.Task.RepairAudioHash, payload.TrackHash = "hash3", "changed-audio"
	if result := d.UpsertTrackDirectly(t.Context(), payload); result.Success {
		t.Fatal("changed audio accepted")
	}
	payload.TrackHash = "hash3"
	if result := d.UpsertTrackDirectly(t.Context(), payload); !result.Success {
		t.Fatalf("repair 3 failed: %+v", result)
	}
	if err := d.pgDB.QueryRow(`SELECT meta,predictions FROM raw.library_flac WHERE id=3`).Scan(&meta, &predictions); err != nil {
		t.Fatal(err)
	}
	if meta != `{"title":"keep"}` || predictions != `{"mood":0.9}` {
		t.Fatal("nonempty JSON overwritten")
	}
}
