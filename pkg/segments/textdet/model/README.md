# Text-detection model

`det.onnx` is PaddleOCR's PP-OCRv5 mobile text-detection model (DB, Apache-2.0)
as ONNX, converted with paddle2onnx by `ilaylow/PP_OCRv5_mobile_onnx` from
`PaddlePaddle/PP-OCRv5_mobile_det`.

- URL: https://huggingface.co/ilaylow/PP_OCRv5_mobile_onnx/resolve/main/ppocrv5_det.onnx
  (repository revision f97b337b3ac256f9dffcac5fc53955082d919d58, 2026-03-19)
- SHA-256: 1eb7b4f7ab657ebd1c66d5f79bca7497f29768a2e3c15e52daecbba1a8e4a039
- Size: 4,826,518 bytes
- Licence: Apache-2.0
- Input `x`: float32 [N, 3, H, W], RGB scaled to [0, 1] then normalized with
  mean (0.485, 0.456, 0.406) and std (0.229, 0.224, 0.225); H and W multiples of 32.
- Output: float32 [N, 1, H, W], each pixel's text probability.

It runs on ONNX Runtime 1.23 (C API 23) through `onnxruntime-purego`.
