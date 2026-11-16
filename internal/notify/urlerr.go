package notify

import (
	"errors"
	"net/url"
)

// unwrapURL drops *url.Error (it prints the request URL, which has the token).
func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
