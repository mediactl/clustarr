# Frame fixtures

20 s of 1 fps, 128×72 8-bit grayscale (`rawvideo`, `gray`), cut on
2026-10-01 from the owner's library with the media image's ffmpeg:

    ffmpeg -ss S -i FILE -t 20 -an -sn -dn -vf fps=1,scale=128:72:flags=area,format=gray -f rawvideo -

| File | Source, start | Shows |
| --- | --- | --- |
| `dexter-credits` | Dexter S01E01, 3100 s | static name cards over a textured grey picture |
| `dexter-scene` | Dexter S01E01, 900 s | a normal scene |
| `bladerunner-credits` | Blade Runner (Final Cut), 6800 s | rolling credits on black |
| `her-credits` | Her (2013), 7300 s | dense rolling credits on black |
| `her-scene` | Her (2013), 3000 s | a normal scene |

Each is 20 × 9,216 bytes; at this size they carry no recognisable picture.
