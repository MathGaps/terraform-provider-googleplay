// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package play

import (
	"errors"
	"net/http"
	"strings"

	"google.golang.org/api/googleapi"
)

// IsNotFound reports whether err is an HTTP 404 from the API.
func IsNotFound(err error) bool {
	return StatusCode(err) == http.StatusNotFound
}

// StatusCode returns the HTTP status of an API error, or 0 when err is not
// one.
func StatusCode(err error) int {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}

	return 0
}

// ErrorDetail renders err for a diagnostic. For an API error it includes the
// response body Google sent, which is where the actionable reason usually is.
func ErrorDetail(err error) string {
	if err == nil {
		return ""
	}

	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return err.Error()
	}

	detail := err.Error()
	body := strings.TrimSpace(apiErr.Body)
	if body != "" && !strings.Contains(detail, body) {
		detail += "\n\nResponse body:\n" + body
	}

	return detail
}
