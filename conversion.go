package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Converts the mp4 into 720p and 360p HLS, as in the HW3 example.
func MP4ToHLS(ctx context.Context, inputPath, outputDir string) error {
	// Create the output folders before ffmpeg writes its files.
	for _, quality := range []string{"0", "1"} {
		if err := os.MkdirAll(filepath.Join(outputDir, quality), 0755); err != nil {
			return err
		}
	}
	// Context lets the worker cancel; -y allows a restarted job to overwrite files.
	return exec.CommandContext(ctx, "ffmpeg", "-y", "-i", inputPath,
		"-map", "0:v:0", "-map", "0:a:0", "-map", "0:v:0", "-map", "0:a:0",
		"-filter:v:0", "scale=-2:720", "-b:v:0", "2500k",
		"-filter:v:1", "scale=-2:360", "-b:v:1", "800k",
		"-f", "hls", "-hls_playlist_type", "vod",
		"-master_pl_name", "master.m3u8",
		"-var_stream_map", "v:0,a:0 v:1,a:1",
		outputDir+"/%v/index.m3u8",
	).Run()
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
