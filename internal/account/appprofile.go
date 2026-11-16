package account

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/beilunyang/openhongguo/adapters/filestore"
	"github.com/beilunyang/openhongguo/adapters/httptransport"
	"github.com/beilunyang/openhongguo/sdk"
)

// AppNickname reads the public App-domain profile name for userID.
//
// The web passport endpoint keeps returning the default "用户<id>" after a
// rename in the mobile app; the App-domain profile carries the current name.
// This is a read-only public lookup with the already registered device, so it
// needs no credentials and never registers another device.
func AppNickname(ctx context.Context, dataDir, userID string) (string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", ErrUpstream
	}
	client, err := sdk.NewClient(sdk.Options{
		Transport:     httptransport.New(),
		ProfileStore:  filestore.New(filepath.Join(dataDir, "profile.json")),
		DeviceResets:  sdk.Int(0),
		RetryAttempts: 1,
	})
	if err != nil {
		return "", err
	}
	person, err := client.Person(ctx, userID)
	if err != nil {
		return "", err
	}
	if person == nil {
		return "", ErrUpstream
	}
	return strings.TrimSpace(person.Name), nil
}

// PreferAppNickname keeps the web profile identity but adopts the current
// App-domain display name when the lookup succeeds. Failure keeps the existing
// name so a renamed account still renders something meaningful offline.
func PreferAppNickname(ctx context.Context, dataDir string, p Profile) Profile {
	name, err := AppNickname(ctx, dataDir, p.UserID)
	if err != nil || name == "" {
		return p
	}
	p.Nickname = name
	return p
}

// traceProfile reports failures only when diagnostics are enabled; it never
// prints the account name, IDs or credentials.
func traceProfile(err error) {
	if os.Getenv("HONGGUO_DIAGNOSTICS") == "" {
		return
	}
	if err == nil {
		fmt.Fprintln(os.Stderr, "GUI account: App profile returned an empty name")
		return
	}
	fmt.Fprintf(os.Stderr, "GUI account: App profile lookup failed: %v\n", err)
}
