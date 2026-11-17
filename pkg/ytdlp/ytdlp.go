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

// Package ytdlp fetches a video from its page with the yt-dlp program: a
// trailer's picture and sound, as the files its site serves them in. It is
// the one place clustarr runs a program that is not itself (exec.go), in
// the one image that holds it (the optional trailers image); the owner
// ruled it in on 2026-11-17, since TMDB and TheTVDB name a trailer only by
// its YouTube page and nothing in Go reads one reliably.
//
// yt-dlp joins a picture and a sound it fetched apart with ffmpeg, which
// no image holds, so it is asked for the two as they are and the caller
// joins them (pkg/transcode/engine.MuxPair).
package ytdlp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrUnavailable: the page has no video to fetch -- removed, private, held
// for an age or a country. Fetching it again will not help.
var ErrUnavailable = errors.New("ytdlp: the video is unavailable")

// ErrThrottled: the site refused this client for now -- a rate limit or a
// check for a person. Every fetch will fail until it relents.
var ErrThrottled = errors.New("ytdlp: the site is refusing requests")

// DefaultTimeout bounds one fetch: a trailer is minutes long and tens of
// megabytes.
const DefaultTimeout = 10 * time.Minute

// maxFileSize stops a page that is not a trailer at all from filling the
// scratch directory.
const maxFileSize = "1G"

// formats asks for the best H.264 picture up to 1080 lines and the best
// AAC sound, each over plain HTTPS and each as its own file (the comma),
// which an MP4 holds without a re-encode; where a site has no such pair,
// its best single MP4 holding both.
const formats = "bv[vcodec^=avc1][height<=1080][protocol^=http],ba[acodec^=mp4a][protocol^=http]/b[ext=mp4][vcodec^=avc1]"

// Fetcher runs yt-dlp.
type Fetcher struct {
	// Path is the yt-dlp program.
	Path string
	// Timeout bounds one fetch; DefaultTimeout when zero.
	Timeout time.Duration
}

// Fetched is what one fetch left in its directory.
type Fetched struct {
	// Video holds the picture. Audio holds the sound when the site served
	// it apart, "" when Video holds both.
	Video, Audio string
}

// Fetch fetches the video at url into dir, which the caller made and
// removes. url is a page built from a site and an id ([trailers.Video.URL]),
// never text from outside.
func (f Fetcher) Fetch(ctx context.Context, url, dir string) (Fetched, error) {
	if f.Path == "" {
		return Fetched{}, errors.New("ytdlp: no yt-dlp program is configured")
	}
	if !strings.HasPrefix(url, "https://") {
		return Fetched{}, fmt.Errorf("ytdlp: %q is not an https page", url)
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr, err := run(ctx, f.Path, dir, args(url, dir))
	if err != nil {
		return Fetched{}, classify(err, stderr)
	}
	return fetched(stdout, dir)
}

// args is the command line of one fetch. Everything after "--" is the page.
func args(url, dir string) []string {
	return []string{
		"--ignore-config", "--no-cache-dir", "--no-playlist", "--no-progress", "--no-part",
		"--no-mtime", "--socket-timeout", "30", "--retries", "3", "--max-filesize", maxFileSize,
		"--paths", dir, "--output", "%(format_id)s.%(ext)s",
		"--format", formats,
		"--print", "after_move:%(vcodec)s\t%(acodec)s\t%(filepath)s",
		"--", url,
	}
}

// fetched reads what yt-dlp printed for each file it wrote: its picture
// codec, its sound codec ("none" for a file without one) and its path.
func fetched(stdout, dir string) (Fetched, error) {
	var out Fetched
	sc := bufio.NewScanner(strings.NewReader(stdout))
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 3)
		if len(parts) != 3 {
			continue
		}
		vcodec, acodec, path := parts[0], parts[1], parts[2]
		// Only a file in the fetch's own directory is one it wrote.
		if filepath.Dir(path) != filepath.Clean(dir) {
			return Fetched{}, fmt.Errorf("ytdlp: a file outside the fetch's directory: %s", path)
		}
		if _, err := os.Stat(path); err != nil {
			return Fetched{}, fmt.Errorf("ytdlp: a file it named is not there: %w", err)
		}
		switch {
		case vcodec != "none" && out.Video == "":
			out.Video = path
		case vcodec == "none" && acodec != "none" && out.Audio == "":
			out.Audio = path
		}
	}
	if out.Video == "" {
		return Fetched{}, errors.New("ytdlp: no file with a picture was fetched")
	}
	return out, nil
}

// classify names why a fetch failed from what yt-dlp wrote: the sentences
// are yt-dlp's own and change with it, so what none of them matches is
// the plain error with its last line.
func classify(err error, stderr string) error {
	low := strings.ToLower(stderr)
	last := lastLine(stderr)
	for _, s := range []string{
		"not a bot", "http error 429", "too many requests", "rate-limited", "rate limited",
	} {
		if strings.Contains(low, s) {
			return fmt.Errorf("%w: %s", ErrThrottled, last)
		}
	}
	for _, s := range []string{
		"video unavailable", "private video", "has been removed", "is not available", "no longer available",
		"confirm your age", "age-restricted", "members-only", "requested format is not available",
		"this video is unavailable", "account associated with this video has been terminated",
		// Vimeo, which serves nothing to a client that is not signed in.
		"only works when logged-in",
	} {
		if strings.Contains(low, s) {
			return fmt.Errorf("%w: %s", ErrUnavailable, last)
		}
	}
	if last == "" {
		return fmt.Errorf("ytdlp: %w", err)
	}
	return fmt.Errorf("ytdlp: %w: %s", err, last)
}

// lastLine is the last line of s that says anything, cut to a log line's
// length.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			if len(l) > 300 {
				l = l[:300]
			}
			return l
		}
	}
	return ""
}
