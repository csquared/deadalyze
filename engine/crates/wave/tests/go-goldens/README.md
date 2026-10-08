# Go goldens for the waves

What deadcatalog's Go `waveform.StreamWaveform` made of each file in
`audio/`, kept for reference now that the Swift port in DEADCA7 is gone
(2026-10-08): per file, `<name>.json` with the seven waves (base64 `data`),
the band scales, the duration and the frames as DEADCA7's decoder shaped
them (not the Go's: the frames were re-made with the record's own shaping,
see `crates/wave/src/lib.rs`), and `<name>.pcm.sha256`, what the runtime's
ffmpeg decodes the file to. The audio is Homebrew ffmpeg 8 `-f lavfi`:

- tone-short.wav: `sine=frequency=440:duration=0.4`, mono 44.1k
- silence.wav: `anullsrc=r=44100:cl=mono`, 1 s
- noise.wav: `anoisesrc=d=3:c=pink:seed=7:a=0.5:r=44100`
- loud-noise.wav: `anoisesrc=d=4:c=white:seed=11:a=0.98:r=44100`
- sweep.wav: `aevalsrc='0.7*sin(2*PI*(30*t+1300*t*t))':s=44100:d=8`
- mix.wav: `aevalsrc='MIX':s=44100:d=6`, MIX a 55 Hz kick every half second,
  an 880 Hz tone on alternate halves and a 7 kHz tick
- stereo48k.wav: `aevalsrc='0.5*sin(2*PI*220*t)|0.4*sin(2*PI*3300*t)':s=48000:d=3`
- delay.mp3: MIX for 6 s at 128k; long.mp3: MIX for 60 s at 64k

The crate's own golden test (`tests/golden.rs`) holds the renderer to
`fixtures/synthetic.json`, the same Go output in hex; these are the
same contract on more material, not yet wired into a test.

`TrigGolden.swift.txt` and `BiquadTests.swift.txt` are the Swift port's
bit-for-bit goldens for Go's `math.Sin`/`math.Cos` and the biquad filters
(the Go probes' output is inline), kept as reference for the Rust port.
