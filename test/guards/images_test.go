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

package guards

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// imageRef matches a fully-qualified clustarr image path, without its tag.
var imageRef = regexp.MustCompile(`ghcr\.io/mediactl/clustarr(?:/[a-z0-9._-]+)*`)

// TestChartImagesMatchConfig holds the Helm chart's image paths to the ones
// config/ actually deploys.
//
// The two are maintained by hand in different files and nothing connected
// them, so they drifted: config/, the Makefile and images/ all used the
// path-style `ghcr.io/mediactl/clustarr/media`, while the chart asked for
// `ghcr.io/mediactl/clustarr-media`. A chart install would have pulled an
// image that does not exist, and no test in the tree noticed, because
// TestChartAndKustomizeAgreePerComponent compares Deployments per component
// and the media images are used by transcode's transcode pool Jobs rather
// than by any Deployment.
//
// This test compares the SET of image paths rather than matching them up
// component by component, deliberately. The chart and config express the same
// images through different shapes -- the chart splits registry from
// repository and templates the tag, config writes a literal string -- so a
// structural comparison would couple this test to both layouts. What actually
// matters is that neither side names an image the other has never heard of.
func TestChartImagesMatchConfig(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	// The chart's side: registry joined to each repository under `image:`.
	raw, err := os.ReadFile(filepath.Join(root, "charts", "clustarr", "values.yaml"))
	require.NoError(t, err, "read the chart values")

	var values struct {
		Image map[string]any `json:"image"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &values), "parse the chart values")

	registry, _ := values.Image["registry"].(string)
	require.NotEmpty(t, registry, "chart values have no image.registry")

	chart := map[string]string{}
	for key, v := range values.Image {
		entry, ok := v.(map[string]any)
		if !ok {
			continue // registry, pullPolicy, and anything else scalar
		}
		repo, ok := entry["repository"].(string)
		if !ok || repo == "" {
			continue
		}
		chart[registry+"/"+repo] = key
	}
	require.NotEmpty(t, chart, "found no image repositories in the chart values; this test's premise has changed")

	// config's side: every clustarr image path any manifest references.
	deployed := map[string]bool{}
	for _, dir := range []string{"config"} {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".yaml") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range imageRef.FindAllString(string(b), -1) {
				deployed[m] = true
			}
			return nil
		})
		require.NoError(t, err)
	}
	require.NotEmpty(t, deployed, "found no clustarr image references under config/")

	for path, key := range chart {
		require.True(t, deployed[path],
			"charts/clustarr/values.yaml's image.%s points at %q, which appears nowhere under config/.\n"+
				"The chart and config/ name the same images by hand in different files; when they drift, "+
				"a chart install pulls a tag that was never built. config/ currently deploys:\n  %s",
			key, path, sortedKeys(deployed))
	}
}

// TestTranscoderImagesAreWhatTheTranscodeControllerStampsOntoPools holds the
// chart's image.native repository, and config/manager/manager.yaml's
// CLUSTARR_NATIVE_IMAGE, to the image images/Dockerfile.native builds (the
// one image every transcode pool and download engine runs since the
// installers wave, spec 2026-10-06 §3.11; it was image.transcoder and
// config/manager/transcode.yaml's CLUSTARR_WORKER_IMAGE), and keeps the CUDA
// image gone: nvidia pools run the same image, the NVIDIA container runtime
// injecting the driver's libraries from the host
// (docs/adr/0015-no-cuda-image.md). No Dockerfile builds a *-cuda target,
// none sets NVIDIA_VISIBLE_DEVICES (all would hand every GPU to any pod
// running the image under the nvidia runtime, bypassing the device plugin),
// and Dockerfile.media-cuda, the first CUDA image, stays gone too.
func TestTranscoderImagesAreWhatTheTranscodeControllerStampsOntoPools(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(root, "charts", "clustarr", "values.yaml"))
	require.NoError(t, err, "read the chart values")

	var values struct {
		Image map[string]any `json:"image"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &values), "parse the chart values")

	entry, ok := values.Image["native"].(map[string]any)
	require.True(t, ok, "charts/clustarr/values.yaml has no image.native")
	require.Equal(t, "mediactl/clustarr/native", entry["repository"],
		"charts/clustarr/values.yaml's image.native.repository")
	require.NotContains(t, values.Image, "transcoderCuda", "there is no CUDA image (ADR 0015)")

	manifest, err := os.ReadFile(filepath.Join(root, "config", "manager", "manager.yaml"))
	require.NoError(t, err, "read config/manager/manager.yaml")
	require.Regexp(t, `(?m)- name: CLUSTARR_NATIVE_IMAGE\n\s+value: ghcr\.io/mediactl/clustarr/native:dev\n`, string(manifest),
		"config/manager/manager.yaml's CLUSTARR_NATIVE_IMAGE")
	require.NotContains(t, string(manifest), "CLUSTARR_WORKER_IMAGE_CUDA", "there is no CUDA image (ADR 0015)")
	require.NotContains(t, string(manifest), "native-cuda")
	require.NotContains(t, string(manifest), "transcoder-cuda")

	_, ok = stageOf(t, filepath.Join(root, "images", "Dockerfile.native"), "native")
	require.True(t, ok, "images/Dockerfile.native builds no native target")

	dockerfiles, err := filepath.Glob(filepath.Join(root, "images", "Dockerfile*"))
	require.NoError(t, err)
	require.NotEmpty(t, dockerfiles)
	stage := regexp.MustCompile(`(?im)^FROM\s+\S+\s+AS\s+(\S*cuda\S*)\s*$`)
	for _, f := range dockerfiles {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		if m := stage.FindSubmatch(b); m != nil {
			t.Errorf("%s builds a %s target: there is no CUDA image (ADR 0015)", filepath.Base(f), m[1])
		}
		if regexp.MustCompile(`(?m)^[^#]*NVIDIA_VISIBLE_DEVICES`).Match(b) {
			t.Errorf("%s sets NVIDIA_VISIBLE_DEVICES: the device plugin names a pod's GPU", filepath.Base(f))
		}
	}
	_, err = os.Stat(filepath.Join(root, "images", "Dockerfile.media-cuda"))
	require.True(t, os.IsNotExist(err), "Dockerfile.media-cuda stays gone")
}

// stageOf reads the Dockerfile at path and reports whether it declares the
// stage named name.
func stageOf(t *testing.T, path, name string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	from, _, ok := dockerStage(string(b), name)
	return from, ok
}

// The image the transcode pools run -- the native image since the
// installers wave (W6.5; it was the transcoder image) -- carries no ffmpeg or
// ffprobe executable: the worker runs every transcode and probe in-process
// (ffgo Phase 5), and app/transcode's TestTheWorkerNeverExecsFFmpeg keeps it
// from exec'ing one. Neither the Dockerfile nor stage.sh may put one in the
// image, and stage.sh's last step refuses one that arrives some other way (a
// package the Intel runtime pulls in, say). The Wolfi image that carried
// them, and the transcoder image after it, stay gone.
func TestTheTranscoderImageCarriesNoFFmpegExecutable(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	installs := regexp.MustCompile(`(?m)^[^#\n]*(/usr/bin/ff(mpeg|probe)|bin/ff(mpeg|probe)\s+\S*/usr/bin)`)
	for _, f := range []string{"images/Dockerfile.native", "images/distroless/stage.sh"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		require.NoError(t, err)
		if m := installs.Find(b); m != nil {
			t.Errorf("%s puts an FFmpeg executable in the native image: %q", f, m)
		}
	}
	stage, err := os.ReadFile(filepath.Join(root, "images", "distroless", "stage.sh"))
	require.NoError(t, err)
	assert.Regexp(t, `(?s)for f in ffmpeg ffprobe\b[^;]*; do.*exit 1`, string(stage), "stage.sh refuses a staged ffmpeg or ffprobe")
	for _, gone := range []string{"Dockerfile.transcoder-distroless", "Dockerfile.transcoder"} {
		_, err = os.Stat(filepath.Join(root, "images", gone))
		assert.True(t, os.IsNotExist(err), "%s became Dockerfile.native", gone)
	}
	df, err := os.ReadFile(filepath.Join(root, "images", "Dockerfile.native"))
	require.NoError(t, err)
	for _, name := range []string{"native", "native-amd64", "native-arm64", "native-base"} {
		from, _, ok := dockerStage(string(df), name)
		require.True(t, ok, "the %s stage", name)
		if name == "native-base" {
			assert.Equal(t, "scratch", from, "the native image is a FROM-scratch image")
		}
	}
}

// dockerStage finds the stage named name in the Dockerfile df: the image it
// is built FROM and its instructions up to the next FROM.
func dockerStage(df, name string) (from, body string, ok bool) {
	re := regexp.MustCompile(`(?im)^FROM\s+(?:--platform=\S+\s+)?(\S+)\s+AS\s+` + regexp.QuoteMeta(name) + `\s*$`)
	loc := re.FindStringSubmatchIndex(df)
	if loc == nil {
		return "", "", false
	}
	body = df[loc[1]:]
	if next := regexp.MustCompile(`(?im)^FROM\s`).FindStringIndex(body); next != nil {
		body = body[:next[0]]
	}
	return df[loc[2]:loc[3]], body, true
}

// Every pool, Intel's included, runs the one native image (ADR 0015; the
// transcoder image until the installers wave, W6.5), so that image carries
// the Intel media runtime -- the iHD VAAPI driver and both QSV runtimes,
// which nothing injects at run time the way the NVIDIA runtime injects
// NVIDIA's -- and no Intel-only target remains. The runtime is amd64's
// alone, and so is the environment pointing libva at it: native picks its
// architecture's stage, and arm64's, which stages no libva, sets none of
// libva's environment.
func TestTheOneTranscoderImageCarriesTheIntelStack(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	b, err := os.ReadFile(filepath.Join(root, "images", "Dockerfile.native"))
	require.NoError(t, err)
	df := string(b)
	assert.NotRegexp(t, `(?im)^FROM\s+\S+\s+AS\s+\S*-intel\b`, df, "no Intel-only target")

	from, _, ok := dockerStage(df, "native")
	require.True(t, ok, "the native target")
	assert.Equal(t, "native-${TARGETARCH}", from, "the native target is its architecture's stage")
	for _, arch := range []string{"amd64", "arm64"} {
		from, _, ok := dockerStage(df, "native-"+arch)
		require.True(t, ok, "the native-%s stage", arch)
		assert.Equal(t, "native-base", from, "native-%s builds on native-base", arch)
	}
	_, amd64, _ := dockerStage(df, "native-amd64")
	assert.Contains(t, amd64, "LIBVA_DRIVERS_PATH=/usr/lib/x86_64-linux-gnu/dri", "amd64 points libva at iHD")
	assert.Contains(t, amd64, "LIBVA_DRIVER_NAME=iHD", "amd64 names the iHD driver")
	_, arm64, _ := dockerStage(df, "native-arm64")
	assert.NotContains(t, arm64, "LIBVA_", "arm64 stages no libva, so it sets none of its environment")

	_, base, ok := dockerStage(df, "native-base")
	require.True(t, ok, "the native-base stage")
	assert.NotContains(t, base, "LIBVA_", "libva's environment is amd64's, not every architecture's")
	staging := regexp.MustCompile(`COPY --from=(\S+) /staging/ /`).FindStringSubmatch(base)
	require.NotNil(t, staging, "native-base copies a staging tree")
	_, m, ok := dockerStage(df, staging[1])
	require.True(t, ok, "the %s stage", staging[1])
	for _, pkg := range []string{"intel-media-va-driver-non-free", "libmfx-gen1.2", "libmfx1/bookworm"} {
		assert.Contains(t, m, pkg, "the one image's staging installs %s", pkg)
	}
}
