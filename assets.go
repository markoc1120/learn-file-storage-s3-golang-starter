package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
)

func (cfg apiConfig) ensureAssetsDir() error {
	if _, err := os.Stat(cfg.assetsRoot); os.IsNotExist(err) {
		return os.Mkdir(cfg.assetsRoot, 0755)
	}
	return nil
}

func getAssetPath(mediaType string) string {
	randomKey := make([]byte, 32)
	rand.Read(randomKey)
	randomName := base64.RawURLEncoding.EncodeToString(randomKey)

	ext := mediaTypeExt(mediaType)
	return fmt.Sprintf("%s%s", randomName, ext)
}

func (cfg apiConfig) getAssetDiskPath(assetPath string) string {
	return filepath.Join(cfg.assetsRoot, assetPath)
}

func (cfg apiConfig) getAssetURL(assetPath string) string {
	return fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, assetPath)
}

func (cfg apiConfig) getAssetURLS3(assetPath string) string {
	return fmt.Sprintf(
		"https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, assetPath,
	)
}

func mediaTypeExt(mediaType string) string {
	parts := strings.Split(mediaType, "/")
	if len(parts) != 2 {
		return ".bin"
	}
	return "." + parts[1]
}

func getVideoAspectRatio(filepath string) (string, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams", filepath,
	)
	out := bytes.Buffer{}
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("Couldn't run ffprobe: %v", err)
	}

	var outJSON struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}

	err = json.Unmarshal(out.Bytes(), &outJSON)
	if err != nil {
		return "", fmt.Errorf("Couldn't parse ffprobe result: %v", err)
	}

	if len(outJSON.Streams) == 0 {
		return "", errors.New("No video")
	}

	width := outJSON.Streams[0].Width
	height := outJSON.Streams[0].Height

	if width == 16*height/9 {
		return "16:9", nil
	} else if height == 16*width/9 {
		return "9:16", nil
	}
	return "other", nil
}

func processVideoForFastStart(filePath string) (string, error) {
	outputPath := filePath + ".processing"
	cmd := exec.Command(
		"ffmpeg",
		"-i", filePath,
		"-c", "copy",
		"-movflags", "faststart",
		"-f", "mp4", outputPath,
	)
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("Couldn't run ffmpeg: %v", err)
	}
	return outputPath, nil
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)
	r, err := presignClient.PresignGetObject(
		context.TODO(),
		&s3.GetObjectInput{
			Key:    &key,
			Bucket: &bucket,
		},
		s3.WithPresignExpires(expireTime),
	)
	if err != nil {
		return "", fmt.Errorf("Couldn't presign object, got err: %v", err)
	}
	return r.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}
	parts := strings.Split(*video.VideoURL, ",")
	if len(parts) < 2 {
		return video, nil
	}
	presignedURL, err := generatePresignedURL(cfg.s3Client, parts[0], parts[1], time.Hour)
	if err != nil {
		return video, nil
	}
	video.VideoURL = &presignedURL
	return video, nil
}
