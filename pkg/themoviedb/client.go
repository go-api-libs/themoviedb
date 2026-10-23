package themoviedb

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/go-api-libs/api"
)

var imgBase = &url.URL{
	Scheme: "https",
	Host:   "image.tmdb.org",
	Path:   "/t/p/original/",
}

func (c *Client) GetImage(ctx context.Context, backdropPath string) (io.ReadCloser, error) {
	u := imgBase.JoinPath(backdropPath)
	req := (&http.Request{
		Header: http.Header{
			"Authorization": []string{c.bearer},
			"User-Agent":    []string{c.userAgent},
		},
		Host:       u.Host,
		Method:     http.MethodGet,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		URL:        u,
	}).WithContext(ctx)

	rsp, ia, err := c.do(req)
	if err != nil {
		return nil, err
	}

	switch rsp.StatusCode {
	case http.StatusOK:
		// 200
		return rsp.Body, nil
	default:
		return nil, c.failed(ia, api.NewErrUnknownStatusCode(rsp))
	}
}
