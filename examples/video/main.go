package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"uuid"

	"github.com/MehrdadV/bfl-go"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() (retErr error) {
	if err := os.MkdirAll("outputs", 0o755); err != nil {
		return fmt.Errorf("create outputs directory: %w", err)
	}
	client, err := bfl.New(bfl.WithAPIKey(os.Getenv("BFL_API_KEY")))
	if err != nil {
		return err
	}
	video, err := client.Videos.Generate(
		context.Background(),
		"A fox running through dawn mist",
		bfl.WithVideoAspectRatio("4:3"),
		bfl.WithGenerateAudio(false),
		bfl.WithVideoDuration(5),
	)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, video.Close())
	}()
	filename := "outputs/video-" + uuid.New().String() + ".mp4"
	if err := video.Save(filename); err != nil {
		return err
	} else {
		log.Printf("Video saved to %s", filename)
	}
	return nil
}
