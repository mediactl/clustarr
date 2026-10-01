/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package selfcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/obinnaokechukwu/ffgo"

	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// clipFrames is the trial clip's length: 2 s at 24 fps.
const clipFrames = 48

// nvencArchival are the owner's archival NVENC settings (spec, Spike
// result): under constqp NVENC ignores -cq, so quality is -qp.
var nvencArchival = map[string]string{
	"preset": "p7", "rc": "constqp", "qp": "23", "spatial-aq": "1", "temporal-aq": "1",
}

// leg is one trial encode: the GPU path a class takes for some sources.
type leg struct {
	name string
	run  func(clip string) (int, error)
}

func legs(class Class) []leg {
	switch class {
	case ClassCUDA:
		return []leg{
			{"nvdec-nvenc", func(clip string) (int, error) {
				// The NVENC tier's path: decode on NVDEC, frames stay on the GPU.
				return onDevice(ffgo.HWDeviceTypeCUDA, "", clip, true, "scale_cuda=format=p010le", "hevc_nvenc", nvencArchival)
			}},
			{"upload-nvenc", func(clip string) (int, error) {
				// A source NVDEC cannot decode (Hi10P, 4:2:2): CPU decode, upload.
				return onDevice(ffgo.HWDeviceTypeCUDA, "", clip, false, "hwupload,scale_cuda=format=p010le", "hevc_nvenc", nvencArchival)
			}},
		}
	case ClassIntel:
		node := IntelRenderNode()
		return []leg{
			{"vaapi", func(clip string) (int, error) {
				if node == "" {
					return 0, errors.New("no Intel render node")
				}
				return onDevice(ffgo.HWDeviceTypeVAAPI, node, clip, false, "hwupload,scale_vaapi=format=p010", "hevc_vaapi", nil)
			}},
			{"qsv", func(clip string) (int, error) {
				if node == "" {
					return 0, errors.New("no Intel render node")
				}
				// QSV picks its Intel adapter itself; a render node path is not
				// a QSV device string (that names the implementation, "hw").
				return onDevice(ffgo.HWDeviceTypeQSV, "", clip, false, "hwupload=extra_hw_frames=64,vpp_qsv=format=p010", "hevc_qsv", nil)
			}},
		}
	}
	return nil
}

// Trial is Check plus a real encode on the class's device for each GPU
// path the class takes. It fails when every leg failed, and reports each.
func Trial(ctx context.Context, class Class, dir string) (Report, error) {
	r, err := Check(ctx, class)
	if err != nil {
		return r, err
	}
	ls := legs(class)
	if len(ls) == 0 {
		return r, nil
	}
	_, span := tracing.Start(ctx, "selfcheck.trial")
	defer span.End()
	clip, err := makeClip(dir)
	if err != nil {
		return r, fmt.Errorf("selfcheck: trial clip: %w", err)
	}
	defer os.Remove(clip)
	r.Trials = map[string]string{}
	var failed []string
	for _, l := range ls {
		n, err := l.run(clip)
		if err == nil && n != clipFrames {
			err = fmt.Errorf("%d packets for %d frames", n, clipFrames)
		}
		if err != nil {
			r.Trials[l.name] = err.Error()
			failed = append(failed, l.name+": "+err.Error())
			continue
		}
		r.Trials[l.name] = ""
	}
	if len(failed) == len(ls) {
		sort.Strings(failed)
		return r, fmt.Errorf("selfcheck: every %s trial failed: %s", class, strings.Join(failed, "; "))
	}
	return r, nil
}

// makeClip writes a 256x144 H.264 clip of clipFrames frames into dir.
func makeClip(dir string) (string, error) {
	path := filepath.Join(dir, "selfcheck-trial.mkv")
	m, err := ffgo.NewMuxer(path, "matroska")
	if err != nil {
		return "", err
	}
	defer m.Close()
	tb := ffgo.NewRational(1, 24)
	enc, err := ffgo.NewVideoStreamEncoder(ffgo.VideoStreamEncoderConfig{
		VideoEncoderConfig: ffgo.VideoEncoderConfig{
			EncoderName: "libx264", Width: 256, Height: 144, PixelFormat: ffgo.PixelFormatYUV420P,
			FrameRate: ffgo.NewRational(24, 1), CodecOptions: map[string]string{"preset": "ultrafast"},
		},
		TimeBase: tb, GlobalHeader: m.NeedsGlobalHeader(),
	})
	if err != nil {
		return "", err
	}
	defer enc.Close()
	ms, err := m.AddEncoderStream(enc, ffgo.StreamOptions{})
	if err != nil {
		return "", err
	}
	if err := m.WriteHeader(); err != nil {
		return "", err
	}
	emit := func(p *ffgo.Packet) error { return m.WritePacket(ms, p) }
	for i := int64(0); i < clipFrames; i++ {
		f, err := ffgo.NewVideoFrame(ffgo.PixelFormatYUV420P, 256, 144)
		if err != nil {
			return "", err
		}
		f.SetPTS(i)
		err = enc.Encode(f, emit)
		_ = f.Free()
		if err != nil {
			return "", err
		}
	}
	if err := enc.Flush(emit); err != nil {
		return "", err
	}
	return path, m.WriteTrailer()
}

// onDevice transcodes clip on a device of type t: decoding on it when
// gpuDecode, else on the CPU with the graph uploading; through filters;
// into encoder. It returns the packets encoded.
func onDevice(t ffgo.HWDeviceType, node, clip string, gpuDecode bool, filters, encoder string, opts map[string]string) (int, error) {
	dev, err := ffgo.NewHWDevice(t, node)
	if err != nil {
		return 0, fmt.Errorf("open device: %w", err)
	}
	defer dev.Close()
	d, err := ffgo.NewDecoder(clip)
	if err != nil {
		return 0, err
	}
	defer d.Close()
	vs := d.VideoStream()
	dcfg := &ffgo.StreamDecoderConfig{}
	if gpuDecode {
		dcfg = &ffgo.StreamDecoderConfig{HWDevice: dev, ExtraHWFrames: 16}
	}
	sd, err := d.NewStreamDecoder(vs.Index, dcfg)
	if err != nil {
		return 0, fmt.Errorf("decoder: %w", err)
	}
	defer sd.Close()

	var (
		graph   *ffgo.FilterGraph
		enc     *ffgo.VideoStreamEncoder
		packets int
	)
	defer func() {
		if enc != nil {
			enc.Close()
		}
		if graph != nil {
			graph.Close()
		}
	}()
	emit := func(*ffgo.Packet) error { packets++; return nil }
	encode := func(frames []*ffgo.Frame) error {
		for _, f := range frames {
			if enc == nil {
				if enc, err = ffgo.NewVideoStreamEncoder(ffgo.VideoStreamEncoderConfig{
					VideoEncoderConfig: ffgo.VideoEncoderConfig{
						EncoderName: encoder, Width: vs.Width, Height: vs.Height, FrameRate: vs.FrameRate,
						HWFramesCtx: graph.OutputHWFramesCtx(), CodecOptions: opts,
					},
					TimeBase: sd.TimeBase(),
				}); err != nil {
					return fmt.Errorf("encoder: %w", err)
				}
			}
			err := enc.Encode(*f, emit)
			_ = f.Free()
			if err != nil {
				return fmt.Errorf("encode: %w", err)
			}
		}
		return nil
	}
	filter := func(f ffgo.Frame) error {
		if graph == nil {
			gcfg := ffgo.FilterGraphConfig{
				Width: vs.Width, Height: vs.Height, PixelFmt: ffgo.PixelFormat(f.Format()),
				TimeBase: sd.TimeBase(), Filters: filters, HWFramesCtx: f.HWFramesCtx(),
			}
			if !gpuDecode {
				gcfg.HWDevice = dev
			}
			if graph, err = ffgo.NewFilterGraph(gcfg); err != nil {
				return fmt.Errorf("filters: %w", err)
			}
		}
		out, err := graph.Filter(&f)
		if err != nil {
			return fmt.Errorf("filter: %w", err)
		}
		return encode(out)
	}
	drain := func() error {
		for {
			f, err := sd.Receive()
			if errors.Is(err, ffgo.ErrAgain) || errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			if err := filter(f); err != nil {
				return err
			}
		}
	}
	send := func(p *ffgo.Packet) error {
		for {
			err := sd.Send(p)
			if !errors.Is(err, ffgo.ErrAgain) {
				return err
			}
			if err := drain(); err != nil {
				return err
			}
		}
	}
	for {
		p, err := d.ReadPacket()
		if err != nil {
			return packets, err
		}
		if p == nil {
			break
		}
		if p.StreamIndex() != vs.Index {
			continue
		}
		if err := send(p); err != nil {
			return packets, fmt.Errorf("decode: %w", err)
		}
		if err := drain(); err != nil {
			return packets, err
		}
	}
	if err := send(nil); err != nil {
		return packets, fmt.Errorf("decode: %w", err)
	}
	if err := drain(); err != nil {
		return packets, err
	}
	if graph == nil || enc == nil {
		return packets, errors.New("no frame reached the encoder")
	}
	out, err := graph.Flush()
	if err != nil {
		return packets, fmt.Errorf("filter: %w", err)
	}
	if err := encode(out); err != nil {
		return packets, err
	}
	return packets, enc.Flush(emit)
}

// IntelRenderNode is the first DRM render node whose PCI vendor is Intel
// (0x8086), or "": a node with both an Intel iGPU and an NVIDIA card has
// two, and only Intel's takes VAAPI and QSV.
func IntelRenderNode() string {
	nodes, _ := filepath.Glob("/sys/class/drm/renderD*")
	for _, n := range nodes {
		v, err := os.ReadFile(filepath.Join(n, "device", "vendor"))
		if err == nil && strings.TrimSpace(string(v)) == "0x8086" {
			return "/dev/dri/" + filepath.Base(n)
		}
	}
	return ""
}
