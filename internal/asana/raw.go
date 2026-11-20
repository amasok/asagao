package asana

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/amasok/asagao/internal/asana/model"
)

// Raw sends method path as it is, for `asagao api` (#9): path is relative to BaseURL
// ("/tasks/123?opt_fields=name") and body, if not nil, is sent as the JSON body unchanged.
// It returns the status and body of Asana's answer whatever the status; for a >= 400 one
// err is also set (*model.Error, with AuthHint for a 401), and the body is still returned
// so that the caller can show it.
//
// The request carries the login's token, so it is kept inside BaseURL: a path that could
// leave it (another host, "..", a backslash) is refused before anything is sent, and a
// redirect is not followed (the token would go with it). Retries are those of the other
// calls: 429 always, 5xx except for POST (which may have been applied).
func (c *Client) Raw(ctx context.Context, method, path string, body []byte) (status int, respBody []byte, err error) {
	u, err := c.rawURL(path)
	if err != nil {
		return 0, nil, err
	}
	hc := http.Client{}
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	rep, err := c.send(ctx, &hc, method, u, body, 1, func(status int) bool { return retryable(method, status) })
	if err != nil {
		return 0, nil, err
	}
	if rep.status >= 400 {
		return rep.status, rep.body, c.newError(rep.status, rep.body)
	}
	return rep.status, rep.body, nil
}

// rawURL is BaseURL + path, checked to stay inside BaseURL.
func (c *Client) rawURL(path string) (string, error) {
	if err := model.CheckRawPath(path); err != nil {
		return "", err
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(strings.TrimSuffix(c.BaseURL, "/") + path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", model.ErrRawPath, err)
	}
	prefix := strings.TrimSuffix(base.EscapedPath(), "/") + "/"
	if u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.Opaque != "" ||
		!strings.HasPrefix(u.EscapedPath(), prefix) {
		return "", model.ErrRawPath
	}
	return u.String(), nil
}
