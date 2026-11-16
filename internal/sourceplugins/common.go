// Package sourceplugins implements the independent source executables. Source
// SDK models, credentials and media preparation never cross the plugin boundary.
package sourceplugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/beilunyang/openhongguo/sdk"
	"github.com/shi747826/mygo-plugin-hongguo/internal/danmaku"
	"github.com/shi747826/mygo-plugin-hongguo/internal/playback"
	"github.com/shi747826/mygo-plugin-hongguo/pluginapi"
)

func params(raw json.RawMessage) (pluginapi.Params, error) {
	var p pluginapi.Params
	if len(raw) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, pluginapi.ErrInvalidOptions
	}
	return p, nil
}

// classify preserves safe, useful diagnostics while replacing private SDK error
// identities with the source-independent error vocabulary.
func classify(err error) error {
	if err == nil {
		return nil
	}
	pairs := []struct{ from, to error }{
		{context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded},
		{playback.ErrClosed, pluginapi.ErrClosed},
		{sdk.ErrAnonymousLimited, pluginapi.ErrAnonymousLimited}, {sdk.ErrDeviceRejected, pluginapi.ErrAnonymousLimited},
		{sdk.ErrRateLimited, pluginapi.ErrRateLimited}, {sdk.ErrAuthenticationRequired, pluginapi.ErrAuthRequired},
		{sdk.ErrContentUnavailable, pluginapi.ErrContentUnavailable}, {sdk.ErrInvalidOptions, pluginapi.ErrInvalidOptions},
		{sdk.ErrParamInvalid, pluginapi.ErrInvalidOptions}, {sdk.ErrDRMProtected, pluginapi.ErrDRMProtected},
		{sdk.ErrTransport, pluginapi.ErrTransport}, {sdk.ErrEndOfList, pluginapi.ErrEndOfList},
		{danmaku.ErrInvalidOptions, pluginapi.ErrInvalidOptions}, {danmaku.ErrRejected, pluginapi.ErrUpstream},
		{danmaku.ErrRequestFailed, pluginapi.ErrTransport},
	}
	for _, p := range pairs {
		if errors.Is(err, p.from) {
			return fmt.Errorf("%s: %w", err.Error(), p.to)
		}
	}
	return err
}
