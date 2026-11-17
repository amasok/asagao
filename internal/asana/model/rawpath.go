package model

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrRawPath is the error for a path that `asagao api` (asana.Client.Raw) refuses to send.
var ErrRawPath = errors.New("asana: パスは Asana の API の中を指す \"/\" で始まる形にしてください (例: /users/me)")

// CheckRawPath reports whether path may be sent with the login's token by `asagao api`:
// it starts with "/" (not "//"), has no space, control character, backslash or fragment,
// and no "." or ".." segment (also when percent-encoded), so that it cannot point outside
// the API. asana.Client.Raw checks it again, with the URL it builds.
func CheckRawPath(path string) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return ErrRawPath
	}
	for _, r := range path {
		if r <= ' ' || r == 0x7f || r == '\\' || r == '#' {
			return ErrRawPath
		}
	}
	p, _, _ := strings.Cut(path, "?")
	dec, err := url.PathUnescape(p)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRawPath, err)
	}
	if strings.ContainsAny(dec, "\\?#") {
		return ErrRawPath
	}
	for _, r := range dec {
		if r < ' ' || r == 0x7f {
			return ErrRawPath
		}
	}
	for _, seg := range strings.Split(dec, "/") {
		if seg == "." || seg == ".." {
			return ErrRawPath
		}
	}
	return nil
}
