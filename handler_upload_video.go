package main

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"slices"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func getSupportedMimeTypesForVideo() []string {
	return []string{"video/mp4"}
}

func getAspectRatioType(aspectRatio string) string {
	switch aspectRatio {
	case "16:9":
		return "landscape"
	case "9:16":
		return "portrait"
	default:
		return "other"
	}
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	const maxMemory = 1 << 30
	r.Body = http.MaxBytesReader(w, r.Body, maxMemory)

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't retrieve video from db", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "You can't do that", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Bad Content-Type in the header", err)
		return
	}

	if !slices.Contains(getSupportedMimeTypesForVideo(), mediaType) {
		respondWithError(w, http.StatusBadRequest, "Not allowed media type", nil)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create temp file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't copy uploaded video to temp file", err)
		return
	}

	tempFile.Seek(0, io.SeekStart)
	processedFilePath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't process video with ffmpeg", err)
		return
	}
	defer os.Remove(processedFilePath)

	processedFile, err := os.Open(processedFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't open processed video", err)
		return
	}
	defer processedFile.Close()

	assetPath := getAssetPath(mediaType)
	aspectRatio, err := getVideoAspectRatio(processedFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get aspect ratio of the video", err)
		return
	}
	assetPath = fmt.Sprintf("%s/%s", getAspectRatioType(aspectRatio), assetPath)
	_, err = cfg.s3Client.PutObject(
		context.TODO(),
		&s3.PutObjectInput{
			Bucket:      &cfg.s3Bucket,
			Key:         &assetPath,
			Body:        processedFile,
			ContentType: &mediaType,
		},
	)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't uplaod video to S3", err)
		return
	}

	videInfoS3 := fmt.Sprintf("%s,%s", cfg.s3Bucket, assetPath)
	video.VideoURL = &videInfoS3

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	video, err = cfg.dbVideoToSignedVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get presigned video", err)
		return
	}
	respondWithJSON(w, http.StatusOK, video)
}
