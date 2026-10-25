package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/go-api-libs/themoviedb/pkg/themoviedb"
)

type Service struct {
	cli *themoviedb.Client
	// other fields, e.g. for caching
}

func NewService() (*Service, error) {
	cli, err := themoviedb.NewClient(themoviedb.WithDebugFile("debug/themoviedb.json"))
	if err != nil {
		return nil, err
	}

	return &Service{cli: cli}, nil
}

func main() {
	ctx := context.Background()

	s, err := NewService()
	if err != nil {
		log.Fatal(err)
	}

	if err := s.SearchMovies(ctx); err != nil {
		log.Fatal(err)
	}
}

func (s *Service) SearchMovies(ctx context.Context) error {
	res, err := s.cli.SearchMovies(ctx, themoviedb.SearchMoviesParams{Query: "The Return of the King"})
	if err != nil {
		return err
	}

	for _, res := range res.Results {
		fmt.Printf("Title: %s\n", res.OriginalTitle)
		fmt.Printf("Description: %s\n", res.Overview) // i.e. description
		fmt.Printf("Popularity: %v\n", res.Popularity)
		fmt.Printf("Release Date: %v\n", res.ReleaseDate)

		if imgPath := res.PosterPath; imgPath != "" {
			backdrop, err := s.cli.GetImage(ctx, imgPath)
			if err != nil {
				return err
			}
			defer backdrop.Close()

			f, err := os.Create(filepath.Base(imgPath))
			if err != nil {
				return err
			}
			defer f.Close()

			if _, err := io.Copy(f, backdrop); err != nil {
				return err
			}
		}

		fmt.Println("------------")
	}

	return nil
}
