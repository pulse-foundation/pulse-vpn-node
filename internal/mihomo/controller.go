package mihomo

import (
	"bytes"
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

// Apply sends bytes already validated by the pinned binary. The controller's
// response alone does not verify that listener creation succeeded.
func (c *Controller) Apply(ctx context.Context, config []byte) error {
	data, err := json.Marshal(struct {
		Payload string `json:"payload"`
	}{string(config)})
	if err != nil {
		return errors.New("cannot encode core reload")
	}
	res, err := c.request(ctx, http.MethodPut, "/configs?force=true", data)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return errors.New("core reload failed; reconcile desired state")
	}
	return nil
}

func (c *Controller) request(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid core controller request")
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("core controller request failed")
	}
	return res, nil
}
