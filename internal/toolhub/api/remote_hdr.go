package api

import (
	"fmt"
	"strings"
)

func validateHeaderDelivery(input CredentialInput) error {
	if input.Delivery != "http_header" {
		return fmt.Errorf("%w: remote credential %s must deliver as an HTTP header", ErrInvalid, input.Name)
	}
	if !headerNamePattern.MatchString(input.Target) || len(input.Target) > 64 || strings.EqualFold(input.Target, "host") || strings.EqualFold(input.Target, "content-length") {
		return fmt.Errorf("%w: invalid credential header %q", ErrInvalid, input.Target)
	}
	if len(input.Prefix) > 32 || strings.ContainsAny(input.Prefix, "\r\n\x00") {
		return fmt.Errorf("%w: invalid credential header prefix", ErrInvalid)
	}
	return nil
}
