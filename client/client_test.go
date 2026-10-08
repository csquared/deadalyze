package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// engineForTest is the engine to test against: DEADCA7_BUNDLE with the
// built engine at ../engine/target/release/engine when the bundle has
// none of its own. Without a bundle the live tests skip.
func engineForTest(t *testing.T) *Engine {
	t.Helper()
	if os.Getenv("DEADCA7_ENGINE") == "" {
		built, _ := filepath.Abs(filepath.Join("..", "engine", "target", "release", "engine"))
		if isExecutable(built) {
			t.Setenv("DEADCA7_ENGINE", built)
		}
	}
	if os.Getenv("DEADCA7_ALGOS") == "" {
		algos, _ := filepath.Abs(filepath.Join("..", "algos"))
		t.Setenv("DEADCA7_ALGOS", algos)
	}
	e, err := Resolve()
	if err != nil {
		t.Skipf("no bundle to test against: %v", err)
	}
	return e
}

func TestDescribe(t *testing.T) {
	e := engineForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, err := e.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Protocols) == 0 || d.Protocols[0] != Protocol {
		t.Fatalf("protocols %v", d.Protocols)
	}
	if d.Recipe == "" || d.Legs["beatnet"].AlgoVersion == "" || d.Limits.MaxBatchItems == 0 {
		t.Fatalf("describe incomplete: %+v", d)
	}
	if _, ok := d.SettingsSchema["arbitrate_min_vote_agreement"]; !ok {
		t.Fatal("settings_schema lacks the recipe's threshold")
	}
}

// TestAnalyzeRefusals needs no audio: a bad protocol is refused before any
// item, and a missing file fails its tasks but the batch completes.
func TestAnalyzeRefusals(t *testing.T) {
	e := engineForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var events []Event
	err := e.Analyze(ctx, Request{Protocol: 99, Items: []Item{{ID: "x", Audio: "/nope.mp3", Tasks: []string{TaskWaveform}}}}, func(ev Event) { events = append(events, ev) })
	if err == nil || len(events) != 1 || events[0].Event != BatchError || events[0].Code != "unsupported_protocol" {
		t.Fatalf("protocol 99: err=%v events=%+v", err, events)
	}

	events = nil
	err = e.Analyze(ctx, Request{Items: []Item{{ID: "x", Audio: "/nope.mp3", Tasks: []string{TaskWaveform, TaskFeatures}}}}, func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	var last Event
	for _, ev := range events {
		kinds = append(kinds, ev.Event)
		last = ev
	}
	if last.Event != BatchDone || last.Failed != 1 {
		t.Fatalf("missing audio: %v; last %+v", kinds, last)
	}
	for i := range events {
		if events[i].Seq != uint64(i+1) {
			t.Fatalf("seq %d at %d", events[i].Seq, i)
		}
	}
	var done Event
	errs := 0
	for _, ev := range events {
		switch ev.Event {
		case ItemError:
			if ev.Code != "audio_missing" {
				t.Fatalf("code %s", ev.Code)
			}
			errs++
		case ItemDone:
			done = ev
		}
	}
	if errs != 2 || done.Tasks["waveform"] != "failed" || done.Tasks["features"] != "failed" {
		t.Fatalf("errors %d, done %+v", errs, done.Tasks)
	}
}

func TestEventItemsField(t *testing.T) {
	var started, finished Event
	if err := json.Unmarshal([]byte(`{"seq":1,"event":"batch_started","items":["a","b"]}`), &started); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"seq":9,"event":"batch_done","items":2,"ok":1,"failed":1}`), &finished); err != nil {
		t.Fatal(err)
	}
	if len(started.ItemIDs) != 2 || finished.Items != 2 || finished.Failed != 1 {
		t.Fatalf("%+v %+v", started, finished)
	}
}
