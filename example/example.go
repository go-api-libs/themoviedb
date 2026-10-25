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

	if err := s.SearchTVShows(ctx); err != nil {
		log.Fatal(err)
	}
}

func (s *Service) SearchMovies(ctx context.Context) error {
	res, err := s.cli.SearchMovies(ctx, themoviedb.SearchMoviesParams{Query: "The Return of the King"})
	if err != nil {
		return err
	}

	for _, res := range res.Results {
		fmt.Printf("res.ID: %v\n", res.ID)
		fmt.Printf("Title: %s\n", res.OriginalTitle)
		fmt.Printf("Description: %s\n", res.Overview) // i.e. description
		fmt.Printf("Popularity: %v\n", res.Popularity)
		fmt.Printf("Release Date: %v\n", res.ReleaseDate)

		if imgPath := res.PosterPath; imgPath != "" {
			if err := s.saveImage(ctx, imgPath); err != nil {
				return err
			}
		}

		fmt.Println("------------")
	}

	return nil
}

func (s *Service) SearchTVShows(ctx context.Context) error {
	res, err := s.cli.SearchTvShows(ctx, themoviedb.SearchTvShowsParams{Query: "The Crown"})
	if err != nil {
		return err
	}

	for _, show := range res.Results {
		fmt.Printf("Name: %s\n", show.Name)
		fmt.Printf("Description: %s\n", show.Overview) // i.e. description
		fmt.Printf("Popularity: %v\n", show.Popularity)
		fmt.Printf("First Air Date: %v\n", show.FirstAirDate)

		if imgPath := show.PosterPath; imgPath != "" {
			if err := s.saveImage(ctx, imgPath); err != nil {
				return err
			}
		}

		season1, err := s.cli.GetTvShowSeason(ctx, show.ID, 1, &themoviedb.GetTvShowSeasonParams{})
		if err != nil {
			return err
		}

		fmt.Printf("season1.ID: %v\n", season1.ID)
		fmt.Printf("season1.UnderscoreID: %v\n", season1.UnderscoreID)
		fmt.Printf("season1.Name: %v\n", season1.Name)
		fmt.Printf("season1.PosterPath: %v\n", season1.PosterPath)
		fmt.Printf("season1.AirDate: %v\n", season1.AirDate)

		for _, ep := range season1.Episodes {
			fmt.Println("---")

			fmt.Printf("ep.ID: %v\n", ep.ID)
			fmt.Printf("ep.Name: %v\n", ep.Name)
			fmt.Printf("ep.Overview: %v\n", ep.Overview)
			fmt.Printf("ep.EpisodeType: %v\n", ep.EpisodeType)

			if imgPath := ep.StillPath; imgPath != "" {
				if err := s.saveImage(ctx, imgPath); err != nil {
					return err
				}
			}
		}

		break
	}

	return nil
}

func (s *Service) saveImage(ctx context.Context, path string) error {
	img, err := s.cli.GetImage(ctx, path)
	if err != nil {
		return err
	}
	defer img.Close()

	f, err := os.Create(filepath.Base(path))
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, img); err != nil {
		return err
	}

	return err
}
