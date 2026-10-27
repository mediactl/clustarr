# Text-recognition model

`rec.onnx` is PaddleOCR's PP-OCRv5 mobile text-recognition model (SVTR-LCNet,
Apache-2.0) as ONNX, converted with paddle2onnx by `ilaylow/PP_OCRv5_mobile_onnx`
from `PaddlePaddle/PP-OCRv5_mobile_rec` -- the same repository that supplies
`pkg/segments/textdet`'s detector.

- URL: https://huggingface.co/ilaylow/PP_OCRv5_mobile_onnx/resolve/bc3497952c4c5bf3b612b9d9cdeaec5236be146d/ppocrv5_rec.onnx
- SHA-256: 243a0f06d826761323e9045e9b113ab2c191c3aa50565585e628300b8eda0224
- Size: 16,584,916 bytes
- Licence: Apache-2.0
- Input: float32 [N, 3, 48, W], a text line resized to 48 rows, each channel
  (x/255 - 0.5) / 0.5 (a grey image is replicated to the three channels).
- Output: float32 [N, T, 18385], per-step class probabilities: 0 is CTC's
  blank, 1..18383 index `dict.txt`'s lines, 18384 is the space.

`dict.txt` is PaddleOCR's `ppocr/utils/dict/ppocrv5_dict.txt` (18,383
characters: CJK, Latin, Cyrillic, Greek, digits, punctuation, emoji).

- URL: https://raw.githubusercontent.com/PaddlePaddle/PaddleOCR/main/ppocr/utils/dict/ppocrv5_dict.txt
- SHA-256: d1979e9f794c464c0d2e0b70a7fe14dd978e9dc644c0e71f14158cdf8342af1b
- Licence: Apache-2.0

Both run on ONNX Runtime 1.23 (C API 23) through `onnxruntime-purego`, the
library the native image carries for markers.
