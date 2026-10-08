package main

import (
	"context"
	"fmt"

	"github.com/cockroachdb/errors"
	"github.com/csquared/deadalyze/client"
)

// engineKinds are the waveform events the engine emits for one track and
// the section each lands in, as deadcatalog's anlz/export writes them
// (writePWAV, writePWV3-7, writePWVC in anlz/export/write.go).
var engineKinds = map[string]string{
	"mono_preview":       "PWAV",
	"mono_detail":        "PWV3",
	"color_preview":      "PWV4",
	"color_detail":       "PWV5",
	"three_band_preview": "PWV6",
	"three_band_detail":  "PWV7",
	"three_band_scales":  "PWVC",
}

// engineSections analyses audio with the bundle's engine and turns its
// waveforms into the sections a stick export would carry, by FourCC, the
// way deadcatalog's anlz/export does: each kind verbatim as the entries of
// its section, PWAV resampled to its 400 columns, and PWV2 derived from
// the mono preview (export stores no tiny preview of its own).
func engineSections(ctx context.Context, eng *client.Engine, audio string) (map[string][]byte, error) {
	kinds := map[string][]byte{}
	var itemErr error
	req := client.Request{Items: []client.Item{{ID: "track", Audio: audio, Tasks: []string{client.TaskWaveform}}}}
	err := eng.Analyze(ctx, req, func(ev client.Event) {
		switch ev.Event {
		case client.WaveformEvent:
			if ev.Data == nil {
				return
			}
			b, err := ev.Data.Bytes()
			if err != nil {
				itemErr = errors.Errorf("waveform %s: %w", ev.Kind, err)
				return
			}
			kinds[ev.Kind] = b
		case client.ItemError:
			itemErr = errors.Errorf("engine %s (%s): %s", ev.Code, ev.Task, ev.Message)
		}
	})
	if err != nil {
		return nil, err
	}
	if itemErr != nil {
		return nil, itemErr
	}
	out := map[string][]byte{}
	for kind, section := range engineKinds {
		b, ok := kinds[kind]
		if !ok {
			return nil, fmt.Errorf("engine sent no %s waveform", kind)
		}
		out[section] = b
	}
	out["PWAV"] = resampleWaveform(kinds["mono_preview"], 400)
	out["PWV2"] = tinyPreview(kinds["mono_preview"])
	return out, nil
}

// tinyPreview is PWV2 as deadcatalog's export derives it (writePWV2): the
// five-bit heights of the mono preview, resampled to 100 columns by max.
func tinyPreview(waveform []byte) []byte {
	heights := make([]byte, len(waveform))
	for i, v := range waveform {
		heights[i] = v & 0x1f
	}
	return resampleWaveform(heights, 100)
}

// resampleWaveform is deadcatalog's: each target column is the max of the
// source columns that map onto it; a source already at the target length
// is copied.
func resampleWaveform(src []byte, targetLen int) []byte {
	if len(src) == 0 {
		return make([]byte, targetLen)
	}
	if len(src) == targetLen {
		out := make([]byte, len(src))
		copy(out, src)
		return out
	}
	out := make([]byte, targetLen)
	for i := range targetLen {
		srcStart := i * len(src) / targetLen
		srcEnd := (i + 1) * len(src) / targetLen
		if srcEnd <= srcStart {
			srcEnd = srcStart + 1
		}
		if srcEnd > len(src) {
			srcEnd = len(src)
		}
		var max byte
		for _, v := range src[srcStart:srcEnd] {
			if v > max {
				max = v
			}
		}
		out[i] = max
	}
	return out
}
