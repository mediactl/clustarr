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

package ytdlp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// program writes a stand-in for yt-dlp: a shell script that records its
// arguments and environment and then runs body.
func program(t *testing.T, body string) (path, record string) {
	t.Helper()
	dir := t.TempDir()
	path, record = filepath.Join(dir, "yt-dlp"), filepath.Join(dir, "record")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\nenv > " + record + ".env\n" + body + "\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path, record
}

const page = "https://www.youtube.com/watch?v=hnEwJKVWjFM"

// A fetch asks for the picture and the sound as two files, names the page
// after "--", and reads back the files the program says it wrote.
func TestFetchReturnsThePictureAndTheSoundTheProgramWrote(t *testing.T) {
	out := t.TempDir()
	path, record := program(t, `touch "`+out+`/137.mp4" "`+out+`/140.m4a"
printf 'avc1.640028\tnone\t%s/137.mp4\n' "`+out+`"
printf 'none\tmp4a.40.2\t%s/140.m4a\n' "`+out+`"`)
	t.Setenv("CLUSTARR_NATS_URL", "nats://secret@nats:4222")

	got, err := Fetcher{Path: path}.Fetch(context.Background(), page, out)
	require.NoError(t, err)
	assert.Equal(t, Fetched{Video: filepath.Join(out, "137.mp4"), Audio: filepath.Join(out, "140.m4a")}, got)

	args, err := os.ReadFile(record)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	assert.Equal(t, []string{"--", page}, lines[len(lines)-2:], "the page is the only thing after --")
	assert.Contains(t, lines, "--ignore-config")
	assert.Contains(t, lines, formats)
	env, err := os.ReadFile(record + ".env")
	require.NoError(t, err)
	assert.NotContains(t, string(env), "CLUSTARR_NATS_URL", "the program gets none of the agent's environment")
}

// A site that serves one file with both is answered with that file alone.
func TestFetchOfOneFileWithBothHasNoSoundFile(t *testing.T) {
	out := t.TempDir()
	path, _ := program(t, `touch "`+out+`/18.mp4"; printf 'avc1.42001E\tmp4a.40.2\t%s/18.mp4\n' "`+out+`"`)
	got, err := Fetcher{Path: path}.Fetch(context.Background(), page, out)
	require.NoError(t, err)
	assert.Equal(t, Fetched{Video: filepath.Join(out, "18.mp4")}, got)
}

func TestAFailedFetchSaysWhy(t *testing.T) {
	cases := []struct {
		name, stderr string
		is           error
	}{
		{"removed", "ERROR: [youtube] abc: Video unavailable. This video has been removed by the uploader", ErrUnavailable},
		{"private", "ERROR: [youtube] abc: Private video. Sign in if you've been granted access to this video", ErrUnavailable},
		{"age", "ERROR: [youtube] abc: Sign in to confirm your age. This video may be inappropriate for some users.", ErrUnavailable},
		{"a check for a person", "ERROR: [youtube] abc: Sign in to confirm you’re not a bot. Use --cookies-from-browser", ErrThrottled},
		{"a rate limit", "ERROR: unable to download video data: HTTP Error 429: Too Many Requests", ErrThrottled},
		{"anything else", "ERROR: unable to download webpage: <urlopen error timed out>", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := t.TempDir()
			path, _ := program(t, `echo "WARNING: something else" >&2; echo "`+c.stderr+`" >&2; exit 1`)
			_, err := Fetcher{Path: path}.Fetch(context.Background(), page, out)
			require.Error(t, err)
			if c.is != nil {
				assert.ErrorIs(t, err, c.is)
			} else {
				assert.NotErrorIs(t, err, ErrUnavailable)
				assert.NotErrorIs(t, err, ErrThrottled)
			}
			assert.Contains(t, err.Error(), "ERROR:", "the program's last line is in the error")
		})
	}
}

// What the program names outside the fetch's directory, or names and did
// not write, is not taken; a fetch that outlives its bound is ended; and
// only an https page is fetched.
func TestFetchTakesOnlyWhatItCanTrust(t *testing.T) {
	out := t.TempDir()
	elsewhere, _ := program(t, `printf 'avc1\tnone\t/etc/passwd\n'`)
	_, err := Fetcher{Path: elsewhere}.Fetch(context.Background(), page, out)
	require.ErrorContains(t, err, "outside the fetch's directory")

	missing, _ := program(t, `printf 'avc1\tnone\t%s/137.mp4\n' "`+out+`"`)
	_, err = Fetcher{Path: missing}.Fetch(context.Background(), page, out)
	require.ErrorContains(t, err, "not there")

	nothing, _ := program(t, `true`)
	_, err = Fetcher{Path: nothing}.Fetch(context.Background(), page, out)
	require.ErrorContains(t, err, "no file with a picture")

	slow, _ := program(t, `exec sleep 30`)
	start := time.Now()
	_, err = Fetcher{Path: slow, Timeout: 200 * time.Millisecond}.Fetch(context.Background(), page, out)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 20*time.Second)

	_, err = Fetcher{Path: nothing}.Fetch(context.Background(), "--exec=touch /tmp/x", out)
	require.ErrorContains(t, err, "not an https page")
	_, err = Fetcher{}.Fetch(context.Background(), page, out)
	require.Error(t, err)
}
