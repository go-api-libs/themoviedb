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

	if err := s.SearchShows(ctx); err != nil {
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
			if err := s.saveImage(ctx, res.OriginalTitle, imgPath); err != nil {
				return err
			}
		}

		fmt.Println("------------")
	}

	return nil
}

func (s *Service) SearchShows(ctx context.Context) error {
	res, err := s.cli.SearchShows(ctx, themoviedb.SearchShowsParams{Query: "The Crown"})
	if err != nil {
		return err
	}

	for _, show := range res.Results {
		// if you want to get recent changes
		if _, err := s.cli.GetShowChanges(ctx, show.ID, nil); err != nil {
			return err
		}

		fmt.Printf("ID: %v\n", show.ID)
		fmt.Printf("Name: %s\n", show.Name)
		fmt.Printf("Description: %s\n", show.Overview)
		fmt.Printf("Popularity: %v\n", show.Popularity)
		fmt.Printf("First Air Date: %v\n", show.FirstAirDate)

		// save show poster
		if imgPath := show.PosterPath; imgPath != "" {
			if err := s.saveImage(ctx, show.Name, imgPath); err != nil {
				return err
			}
		}

		showDetails, err := s.cli.GetShow(ctx, show.ID, nil)
		if err != nil {
			return err
		}

		fmt.Printf("NumberOfSeasons: %v\n", showDetails.NumberOfSeasons)
		fmt.Printf("NumberOfEpisodes: %v\n", showDetails.NumberOfEpisodes)

		for _, season := range showDetails.Seasons {
			seasonDir := filepath.Join(show.Name, season.Name)

			// if you want to get recent changes
			if _, err := s.cli.GetSeasonChanges(ctx, season.ID, nil); err != nil {
				return err
			}

			seasonDetails, err := s.cli.GetShowSeason(ctx, showDetails.ID, season.SeasonNumber, nil)
			if err != nil {
				return err
			}

			// save season posters
			if imgPath := seasonDetails.PosterPath; imgPath != "" {
				if err := s.saveImage(ctx, seasonDir, imgPath); err != nil {
					return err
				}
			}

			for _, ep := range seasonDetails.Episodes {
				// if you want to get recent changes
				if _, err := s.cli.GetEpisodeChanges(ctx, ep.ID); err != nil {
					return err
				}

				// doesn't happen
				if ep.ShowID != show.ID {
					return fmt.Errorf("id mismatch: %d vs. %d", ep.ShowID, show.ID)
				}

				epDir := filepath.Join(seasonDir, fmt.Sprintf("%02d %s", ep.EpisodeNumber, ep.Name))
				if imgPath := ep.StillPath; imgPath != "" {
					if err := s.saveImage(ctx, epDir, imgPath); err != nil {
						return err
					}
				}

				// get additional details
				_, err := s.cli.GetEpisodeDetails(ctx, show.ID, season.SeasonNumber, ep.EpisodeNumber, nil)
				if err != nil {
					return err
				}

				break
			}
		}

		break
	}

	return nil
}

func (s *Service) saveImage(ctx context.Context, dir, path string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	img, err := s.cli.GetImage(ctx, path)
	if err != nil {
		return err
	}
	defer img.Close()

	f, err := os.Create(filepath.Join(dir, filepath.Base(path)))
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, img); err != nil {
		return err
	}

	return err
}
