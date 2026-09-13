package dispatcher

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

type bufferWriteCloser struct{ bytes.Buffer }

func (*bufferWriteCloser) Close() error { return nil }

func TestDemucsSeparateConsumesStemEventsBeforeFinalResponse(t *testing.T) {
	stdout := strings.Join([]string{
		`{"status":"stem_ready","request_id":"request-9","generation":9,"stem":"vocals","audio_hash":"abc","sr":44100,"info":{"shape":[1,4],"dtype":"float32"}}`,
		`{"status":"stem_ready","request_id":"request-9","generation":9,"stem":"mix","audio_hash":"abc","sr":44100,"info":{"shape":[1,4],"dtype":"float32"}}`,
		`{"status":"success","audio_hash":"abc","sr":44100,"stems":{"vocals":{"shape":[1,4],"dtype":"float32"},"mix":{"shape":[1,4],"dtype":"float32"}}}`,
	}, "\n") + "\n"
	stdin := &bufferWriteCloser{}
	client := &DemucsDaemonClient{
		id:      1,
		stdin:   stdin,
		stdout:  bufio.NewReader(strings.NewReader(stdout)),
		isAlive: true,
	}
	var got []string
	response, err := client.SeparateWithEvents(t.Context(), DemucsSeparatePayload{RequestID: "request-9", Generation: 9}, func(event DemucsStemReadyEvent) error {
		got = append(got, event.Stem)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.AudioHash != "abc" || len(got) != 2 || got[0] != "vocals" || got[1] != "mix" {
		t.Fatalf("events=%v response=%+v", got, response)
	}
	request, err := io.ReadAll(stdin)
	if err != nil || !bytes.Contains(request, []byte(`"request_id":"request-9"`)) || !bytes.Contains(request, []byte(`"generation":9`)) {
		t.Fatalf("request=%s err=%v", request, err)
	}
}

func TestDemucsStemReadyRejectsStaleRequestAndZeroGeneration(t *testing.T) {
	payload := DemucsSeparatePayload{RequestID: "request-8", Generation: 8}
	valid := DemucsStemReadyEvent{Status: "stem_ready", RequestID: "request-8", Generation: 8, Stem: "mix", AudioHash: "hash", SR: 44100, Info: StemInfo{Shape: []int64{1, 4}, Dtype: "float32"}}
	if err := validateDemucsStemReadyEvent(payload, valid); err != nil {
		t.Fatal(err)
	}
	valid.RequestID = "stale"
	if err := validateDemucsStemReadyEvent(payload, valid); err == nil {
		t.Fatal("stale request accepted")
	}
	valid.RequestID = payload.RequestID
	valid.Generation = 0
	if err := validateDemucsStemReadyEvent(payload, valid); err == nil {
		t.Fatal("zero generation accepted")
	}
}
