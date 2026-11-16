package account

import (
	"context"
	"testing"
)

func TestPreferAppNicknameFallsBackWithoutDevice(t *testing.T) {
	// No profile.json in this temporary directory: the App lookup must fail
	// quietly and keep the web profile, which still carries the account identity.
	web := Profile{UserID: "42", Nickname: "用户42", Avatar: "https://example.invalid/a.png"}
	got := PreferAppNickname(context.Background(), t.TempDir(), web)
	if got != web {
		t.Fatalf("fallback changed the profile: %+v", got)
	}
}

func TestPreferAppNicknameNeedsUserID(t *testing.T) {
	got := PreferAppNickname(context.Background(), t.TempDir(), Profile{Nickname: "昵称"})
	if got.Nickname != "昵称" {
		t.Fatalf("unexpected profile: %+v", got)
	}
}
