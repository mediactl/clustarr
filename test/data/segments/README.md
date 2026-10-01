# Segment detection fixtures

`*.s16le` are 15 s of mono 16-bit PCM at 11,025 Hz, generated (no
third-party audio) with the media image's ffmpeg:

- `sweep`: `aevalsrc='0.5*sin(2*PI*(110+200*t)*t)'`
- `chords`: three tones, one amplitude-modulated, one gated every 2 s
- `noise`: `anoisesrc=c=pink:a=0.4:seed=7`

`*.fp` are their Chromaprint fingerprints, raw little-endian `uint32`, from
the same image (ffmpeg n9.0.2, BtbN):

    ffmpeg -f s16le -ar 11025 -ac 1 -i X.s16le -f chromaprint -fp_format raw X.fp

`pkg/segments/chromaprint` must reproduce them bit for bit.
