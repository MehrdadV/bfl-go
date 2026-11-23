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
	if err := os.MkdirAll("outputs", 0o755); err != nil { // #nosec G301 -- Example output directory is intentionally readable.
		return fmt.Errorf("create outputs directory: %w", err)
	}
	client, err := bfl.New(bfl.WithAPIKey(os.Getenv("BFL_API_KEY")))
	if err != nil {
		return err
	}

	ctx := context.Background()
	job, err := client.Images.Submit(ctx, bfl.ImageRequest{Prompt: "A mountain lake at sunset"})
	if err != nil {
		return err
	}
	status, err := job.Status(ctx)
	if err != nil {
		return err
	}
	log.Printf("job %s: %s", status.ID, status.State)

	image, err := job.Wait(ctx)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, image.Close())
	}()
	filename := "outputs/image-jobs-" + uuid.New().String() + ".png"
	if err := image.Save(filename); err != nil {
		return err
	} else {
		log.Printf("Image saved to %s", filename)
	}
	return nil
}
