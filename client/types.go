package client

import (
	"encoding/base64"
	"encoding/json"
	"os"
)

// Describe is `engine describe`.
type Describe struct {
	Protocols      []int                    `json:"protocols"`
	Engine         EngineID                 `json:"engine"`
	Recipe         string                   `json:"recipe"`
	RuntimeVersion string                   `json:"runtime_version,omitempty"`
	Tasks          []string                 `json:"tasks"`
	Commands       []string                 `json:"commands"`
	Legs           map[string]Leg           `json:"legs"`
	Decoder        *Decoder                 `json:"decoder,omitempty"`
	Devices        []string                 `json:"devices"`
	Limits         Limits                   `json:"limits"`
	SettingsSchema map[string]SettingSchema `json:"settings_schema"`
}

type EngineID struct {
	Impl    string `json:"impl"`
	Version string `json:"version"`
}

type Leg struct {
	AlgoVersion string `json:"algo_version"`
	Impl        string `json:"impl"`
	Ready       bool   `json:"ready"`
	Note        string `json:"note,omitempty"`
}

type Decoder struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
}

type Limits struct {
	MaxBatchItems int   `json:"max_batch_items"`
	MaxLineBytes  int64 `json:"max_line_bytes"`
}

type SettingSchema struct {
	Type            string            `json:"type"`
	Default         json.RawMessage   `json:"default"`
	Title           string            `json:"title,omitempty"`
	Unit            string            `json:"unit,omitempty"`
	Minimum         *float64          `json:"minimum,omitempty"`
	Maximum         *float64          `json:"maximum,omitempty"`
	Step            *float64          `json:"step,omitempty"`
	Enum            []json.RawMessage `json:"enum,omitempty"`
	Group           string            `json:"group"`
	AffectsIdentity bool              `json:"affects_identity"`
}

// Request is what Analyze writes to the engine.
type Request struct {
	Protocol int            `json:"protocol"`
	Settings map[string]any `json:"settings,omitempty"`
	Items    []Item         `json:"items"`
}

type Item struct {
	ID      string   `json:"id"`
	Audio   string   `json:"audio"`
	Tasks   []string `json:"tasks"`
	CueAlgo string   `json:"cue_algo,omitempty"`
	Inputs  *Inputs  `json:"inputs,omitempty"`
}

type Inputs struct {
	Grid     *InputGrid     `json:"grid,omitempty"`
	Features *InputFeatures `json:"features,omitempty"`
}

type InputGrid struct {
	BPM         float64    `json:"bpm"`
	BeatsPerBar int        `json:"beats_per_bar"`
	Beats       [][2]int64 `json:"beats"`
}

type InputFeatures struct {
	Format string `json:"format"`
	Data   Blob   `json:"data"`
}

// Blob is bytes inline (b64) or in a file the engine wrote (path).
type Blob struct {
	B64  string `json:"b64,omitempty"`
	Path string `json:"path,omitempty"`
}

// Bytes are the blob's bytes, read from the file for a path blob.
func (b Blob) Bytes() ([]byte, error) {
	if b.Path != "" {
		return os.ReadFile(b.Path)
	}
	return base64.StdEncoding.DecodeString(b.B64)
}

// Inline makes a b64 blob.
func Inline(data []byte) Blob {
	return Blob{B64: base64.StdEncoding.EncodeToString(data)}
}

// Event is one line of engine output. Every field of every event kind is
// here, zero when the kind does not carry it; Raw is the line itself for
// a host that keeps provenance verbatim.
type Event struct {
	Seq   uint64 `json:"seq"`
	Event string `json:"event"`
	ID    string `json:"id,omitempty"`

	// batch_started
	Engine         *EngineID       `json:"engine,omitempty"`
	Recipe         string          `json:"recipe,omitempty"`
	RuntimeVersion string          `json:"runtime_version,omitempty"`
	Settings       json.RawMessage `json:"settings,omitempty"`
	ItemIDs        []string        `json:"-"`

	// batch_done / progress
	Items     int    `json:"-"`
	OK        int    `json:"ok,omitempty"`
	Failed    int    `json:"failed,omitempty"`
	ElapsedMs int64  `json:"elapsed_ms,omitempty"`
	Stage     string `json:"stage,omitempty"`
	Done      int    `json:"done,omitempty"`
	Total     int    `json:"total,omitempty"`

	// item_started
	DurationMs uint32 `json:"duration_ms,omitempty"`

	// waveform_frame / waveform
	Offset           uint32     `json:"offset,omitempty"`
	Columns          uint32     `json:"columns,omitempty"`
	ColumnsPerSecond uint32     `json:"columns_per_second,omitempty"`
	Kind             string     `json:"kind,omitempty"`
	EntryBytes       int        `json:"entry_bytes,omitempty"`
	EntryCount       int        `json:"entry_count,omitempty"`
	Rate             int        `json:"rate,omitempty"`
	Source           string     `json:"source,omitempty"`
	BandScales       *[3]uint16 `json:"band_scales,omitempty"`
	Data             *Blob      `json:"data,omitempty"`

	// grid
	BPM             float64         `json:"bpm,omitempty"`
	BeatsPerBar     int             `json:"beats_per_bar,omitempty"`
	Beats           [][2]int64      `json:"beats,omitempty"`
	FirstBeatMs     *int64          `json:"first_beat_ms,omitempty"`
	FirstDownbeatMs *int64          `json:"first_downbeat_ms,omitempty"`
	Timeline        *Timeline       `json:"timeline,omitempty"`
	Identity        *Identity       `json:"identity,omitempty"`
	RanOn           *RanOn          `json:"ran_on,omitempty"`
	Consensus       *Consensus      `json:"consensus,omitempty"`
	Provenance      json.RawMessage `json:"provenance,omitempty"`

	// key
	Camelot     string         `json:"camelot,omitempty"`
	Label       string         `json:"label,omitempty"`
	Confidence  float64        `json:"confidence,omitempty"`
	Top         []KeyCandidate `json:"top,omitempty"`
	AlgoVersion string         `json:"algo_version,omitempty"`
	Note        string         `json:"note,omitempty"`

	// features
	Format string `json:"format,omitempty"`

	// cues
	Algo string `json:"algo,omitempty"`
	Cues []Cue  `json:"cues,omitempty"`

	// item_warning / item_error / batch_error
	Task      string `json:"task,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`

	// item_done
	Tasks map[string]string `json:"tasks,omitempty"`

	// The line as the engine wrote it.
	Raw []byte `json:"-"`
}

// UnmarshalJSON reads the fields whose names collide across kinds
// (`items` is ids on batch_started and a count on batch_done).
func (e *Event) UnmarshalJSON(b []byte) error {
	type plain Event
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*e = Event(p)
	var items struct {
		Items json.RawMessage `json:"items"`
	}
	if json.Unmarshal(b, &items) == nil && len(items.Items) > 0 {
		if items.Items[0] == '[' {
			_ = json.Unmarshal(items.Items, &e.ItemIDs)
		} else {
			_ = json.Unmarshal(items.Items, &e.Items)
		}
	}
	return nil
}

type Timeline struct {
	Name           string          `json:"name"`
	OffsetMs       float64         `json:"offset_ms"`
	AppliedShiftMs int64           `json:"applied_shift_ms"`
	Decode         *TimelineDecode `json:"decode,omitempty"`
}

type TimelineDecode struct {
	Flags      []string `json:"flags"`
	StartTimeS float64  `json:"start_time_s"`
}

type LegIdentity struct {
	AlgoVersion string `json:"algo_version"`
	CfgHash     string `json:"cfg_hash"`
}

type Identity struct {
	Recipe       string                 `json:"recipe"`
	Legs         map[string]LegIdentity `json:"legs"`
	Decoder      string                 `json:"decoder"`
	IdentityHash string                 `json:"identity_hash"`
}

type RanOn struct {
	Device         string   `json:"device"`
	RuntimeVersion string   `json:"runtime_version,omitempty"`
	Engine         EngineID `json:"engine"`
}

type BeatThisVote struct {
	BPM            float64 `json:"bpm"`
	FirstBeatMs    *int64  `json:"first_beat_ms"`
	PhaseVote      int     `json:"phase_vote"`
	PhaseAgreement float64 `json:"phase_agreement"`
	ResidStdev     float64 `json:"resid_stdev"`
}

type Consensus struct {
	Verdict      string        `json:"verdict"`
	Dispute      string        `json:"dispute,omitempty"`
	ShiftMs      *int64        `json:"shift_ms,omitempty"`
	RelabelBeats *int64        `json:"relabel_beats,omitempty"`
	BeatThis     *BeatThisVote `json:"beat_this,omitempty"`
	Note         string        `json:"note,omitempty"`
}

type KeyCandidate struct {
	Label string  `json:"label"`
	P     float64 `json:"p"`
}

type Cue struct {
	Comment string `json:"comment,omitempty"`
	HotCue  int    `json:"hot_cue"`
	TimeMs  int64  `json:"time_ms"`
}

// Event kinds.
const (
	BatchStarted  = "batch_started"
	BatchDone     = "batch_done"
	BatchError    = "batch_error"
	Progress      = "progress"
	ItemStarted   = "item_started"
	WaveformFrame = "waveform_frame"
	WaveformEvent = "waveform"
	GridEvent     = "grid"
	KeyEvent      = "key"
	FeaturesEvent = "features"
	CuesEvent     = "cues"
	ItemWarning   = "item_warning"
	ItemError     = "item_error"
	ItemDone      = "item_done"
)

// Tasks.
const (
	TaskGrid     = "grid"
	TaskKey      = "key"
	TaskFeatures = "features"
	TaskWaveform = "waveform"
	TaskCues     = "cues"
)
