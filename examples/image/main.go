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
	image, err := client.Images.Generate(context.Background(), "A mountain lake at sunset")
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, image.Close())
	}()

	filename := "outputs/" + uuid.New().String() + ".png"
	if err := image.Save(filename); err != nil {
		return err
	} else {
		log.Printf("Image saved to %s", filename)
	}
	return nil
}
