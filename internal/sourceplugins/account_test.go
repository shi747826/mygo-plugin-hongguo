package sourceplugins

import (
	"context"
	"errors"
	"github.com/shi747826/mygo-plugin-hongguo/internal/account"
	"github.com/shi747826/mygo-plugin-hongguo/pluginapi"
	"testing"
)

type blockedProfileClient struct {
	*account.Client
	started chan struct{}
	proceed chan struct{}
}

func (f *blockedProfileClient) Profile(context.Context) (account.Profile, error) {
	close(f.started)
	<-f.proceed
	return account.Profile{UserID: "123", Nickname: "test"}, nil
}
func (f *blockedProfileClient) CookieHeader() string { return "sessionid=synthetic" }

func TestLogoutPreventsLateProfileSessionWrite(t *testing.T) {
	dir := t.TempDir()
	a, err := newHongguoAccount(dir)
	if err != nil {
		t.Fatal(err)
	}
	fake := &blockedProfileClient{Client: account.New(), started: make(chan struct{}), proceed: make(chan struct{})}
	a.client = fake
	done := make(chan error, 1)
	go func() { _, err := a.handle(context.Background(), "AccountProfile", nil); done <- err }()
	<-fake.started
	if _, err = a.handle(context.Background(), "AccountLogout", nil); err != nil {
		t.Fatal(err)
	}
	close(fake.proceed)
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("late profile was accepted: %v", err)
	}
	if _, ok, err := account.LoadSession(dir); err != nil || ok {
		t.Fatalf("old session restored after logout: ok=%v err=%v", ok, err)
	}
}
func TestCanceledProfileDoesNotSaveSession(t *testing.T) {
	dir := t.TempDir()
	a, err := newHongguoAccount(dir)
	if err != nil {
		t.Fatal(err)
	}
	fake := &blockedProfileClient{Client: account.New(), started: make(chan struct{}), proceed: make(chan struct{})}
	a.client = fake
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := a.handle(ctx, "AccountProfile", nil); done <- err }()
	<-fake.started
	cancel()
	close(fake.proceed)
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled profile accepted: %v", err)
	}
	if _, ok, err := account.LoadSession(dir); err != nil || ok {
		t.Fatalf("canceled profile persisted: ok=%v err=%v", ok, err)
	}
}
func TestAccountErrorsBecomeNeutral(t *testing.T) {
	a, err := newHongguoAccount(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// No HTTP request is needed for malformed account RPC arguments.
	_, err = a.handle(context.Background(), "AccountLoginWait", []byte(`{"Token":12}`))
	if !errors.Is(err, pluginapi.ErrInvalidOptions) {
		t.Fatal(err)
	}
}
