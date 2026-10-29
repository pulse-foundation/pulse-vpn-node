package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

const Version = "v1.19.33"

type Controller struct {
	url, secret string
	client      *http.Client
}

func NewController(url, secret string) *Controller {
	return &Controller{url: url, secret: secret, client: &http.Client{
		Timeout: time.Second,
		// Do not inherit proxy environment variables or forward credentials on redirects.
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *Controller) Version(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/version", nil)
	if err != nil {
		return "", errors.New("invalid core controller URL")
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	res, err := c.client.Do(req)
	if err != nil {
		return "", errors.New("core controller unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("core controller rejected readiness check")
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&info); err != nil {
		return "", errors.New("invalid core version response")
	}
	if info.Version != Version {
		return "", errors.New("core version does not match pinned version")
	}
	return info.Version, nil
}
