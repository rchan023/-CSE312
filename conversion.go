package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The command receives separate arguments, not a shell command string.
func MP4ToHLS(ctx context.Context, inputPath, outputDir string) error {
	for _, quality := range []string{"0", "1"} {
		if err := os.MkdirAll(filepath.Join(outputDir, quality), 0755); err != nil {
			return err
		}
	}
	probe, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=index", "-of", "csv=p=0", inputPath).Output()
	if err != nil {
		return fmt.Errorf("could not inspect video")
	}
	audio := strings.TrimSpace(string(probe)) != ""
	args := []string{"-y", "-nostdin", "-hide_banner", "-loglevel", "error", "-filter_threads", "1", "-i", inputPath, "-map", "0:v:0"}
	if audio {
		args = append(args, "-map", "0:a:0")
	}
	args = append(args, "-map", "0:v:0")
	if audio {
		args = append(args, "-map", "0:a:0")
	}
	args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p",
		"-filter:v:0", "scale=-2:720", "-b:v:0", "2500k", "-filter:v:1", "scale=-2:360", "-b:v:1", "800k",
		"-threads:v:0", "1", "-threads:v:1", "1", "-force_key_frames", "expr:gte(t,n_forced*4)")
	streams := "v:0 v:1"
	if audio {
		args = append(args, "-c:a", "aac", "-b:a", "128k", "-ac", "2", "-ar", "48000")
		streams = "v:0,a:0 v:1,a:1"
	}
	args = append(args, "-f", "hls", "-hls_time", "4", "-hls_playlist_type", "vod", "-hls_flags", "independent_segments",
		"-master_pl_name", "master.m3u8", "-var_stream_map", streams,
		"-hls_segment_filename", filepath.Join(outputDir, "%v", "seg_%05d.ts"), filepath.Join(outputDir, "%v", "index.m3u8"))
	if err := exec.CommandContext(ctx, "ffmpeg", args...).Run(); err != nil {
		return fmt.Errorf("video conversion failed")
	}
	return nil
}

// Two workers poll a durable queue in PostgreSQL, so uploads never block on a full channel.
func (m *MediaHandlers) StartWorkers(ctx context.Context) error {
	timer, cancel := dbTimer()
	defer cancel()
	if err := m.resumeConversions(timer); err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		go m.conversionWorker(ctx)
	}
	return nil
}
func (m *MediaHandlers) conversionWorker(ctx context.Context) {
	for ctx.Err() == nil {
		id, err := m.claimVideo(ctx)
		if err == sql.ErrNoRows || err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		timeout, cancel := context.WithTimeout(ctx, 2*time.Hour)
		directory := filepath.Join(m.Directory, "videos", id)
		err = MP4ToHLS(timeout, filepath.Join(directory, "original.mp4"), filepath.Join(directory, "hls"))
		cancel()
		status, path := "ready", "/media/videos/"+id+"/hls/master.m3u8"
		if err != nil {
			status = "failed"
			path = ""
			fmt.Println("Video conversion failed:", id)
		}
		timer, stop := dbTimer()
		_, updateErr := m.DB.ExecContext(timer, "UPDATE videos SET status=$1,hls_path=$2 WHERE id=$3", status, path, id)
		stop()
		if updateErr != nil {
			fmt.Println("Could not save conversion status:", id)
		}
	}
}
func (m *MediaHandlers) claimVideo(ctx context.Context) (string, error) {
	timer, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var id string
	err := m.DB.QueryRowContext(timer, `
 UPDATE videos SET status='processing' WHERE id=(
  SELECT id FROM videos WHERE status='queued' ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
 ) RETURNING id`).Scan(&id)
	return id, err
}
