package auth

import (
	"errors"

	"golang.org/x/oauth2"
)

// TokenError is returned by Source.Token when no token could be had for a request: nobody
// is logged in (ErrNotLoggedIn), another login replaced ours (ErrLoginReplaced), or the
// OAuth refresh failed (*oauth2.RetrieveError). The request was not sent.
//
// It shows and unwraps to the error it holds, so errors.Is and errors.As find that error
// as before. The sync engine knows it by its RequestNotSent method, without importing auth
// (ADR 0004: x/oauth2 stays in auth).
type TokenError struct{ Err error }

func (e *TokenError) Error() string { return e.Err.Error() }
func (e *TokenError) Unwrap() error { return e.Err }

// RequestNotSent reports that the request did not reach Asana.
func (e *TokenError) RequestNotSent() bool { return true }

// Token returns a token for a request, from the source the login uses. When there is none
// to send (see TokenError), the error is a *TokenError; other errors are returned as they
// are.
func (s *Source) Token() (*oauth2.Token, error) {
	tok, err := s.TokenSource.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.Is(err, ErrNotLoggedIn) || errors.Is(err, ErrLoginReplaced) || errors.As(err, &re) {
			return nil, &TokenError{Err: err}
		}
		return nil, err
	}
	return tok, nil
}
