package pluginapi

import (
	"context"
	"errors"
	"sort"
)

// Error preserves neutral error classifications across the process boundary.
type Error struct {
	Code    string   `json:"code,omitempty"`
	Message string   `json:"message"`
	Kinds   []string `json:"kinds,omitempty"`
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}
func (e *Error) Is(target error) bool {
	if t, ok := target.(*Error); ok {
		if e.Code == t.Code && t.Code != "" {
			return true
		}
		for _, k := range e.Kinds {
			if k == t.Code {
				return true
			}
		}
	}
	if target == context.Canceled {
		return e.has("canceled")
	}
	if target == context.DeadlineExceeded {
		return e.has("deadline")
	}
	return false
}
func (e *Error) has(code string) bool {
	if e.Code == code {
		return true
	}
	for _, k := range e.Kinds {
		if k == code {
			return true
		}
	}
	return false
}

var (
	ErrUnsupported        = &Error{Code: "unsupported"}
	ErrClosed             = &Error{Code: "closed"}
	ErrProtocol           = &Error{Code: "protocol"}
	ErrAnonymousLimited   = &Error{Code: "anonymous_limited"}
	ErrRateLimited        = &Error{Code: "rate_limited"}
	ErrAuthRequired       = &Error{Code: "auth_required"}
	ErrContentUnavailable = &Error{Code: "content_unavailable"}
	ErrTransport          = &Error{Code: "transport"}
	ErrInvalidOptions     = &Error{Code: "invalid_options"}
	ErrDRMProtected       = &Error{Code: "drm_protected"}
	ErrNotFound           = &Error{Code: "not_found"}
	ErrUpstream           = &Error{Code: "upstream"}
	ErrTimeout            = &Error{Code: "timeout"}
	ErrEndOfList          = &Error{Code: "end_of_list"}
)
var classifications = []error{ErrUnsupported, ErrClosed, ErrProtocol, ErrAnonymousLimited, ErrRateLimited, ErrAuthRequired, ErrContentUnavailable, ErrTransport, ErrInvalidOptions, ErrDRMProtected, ErrNotFound, ErrUpstream, ErrTimeout, ErrEndOfList}

func EncodeError(err error) *Error {
	if err == nil {
		return nil
	}
	w := &Error{Message: err.Error()}
	kinds := map[string]bool{}
	var e *Error
	if errors.As(err, &e) {
		w.Code = e.Code
		for _, k := range e.Kinds {
			kinds[k] = true
		}
	}
	for _, sentinel := range classifications {
		if errors.Is(err, sentinel) {
			kinds[sentinel.(*Error).Code] = true
		}
	}
	if errors.Is(err, context.Canceled) {
		kinds["canceled"] = true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		kinds["deadline"] = true
	}
	for k := range kinds {
		w.Kinds = append(w.Kinds, k)
	}
	sort.Strings(w.Kinds)
	return w
}
