package sourceplugins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/shi747826/mygo-plugin-hongguo/internal/account"
	"github.com/shi747826/mygo-plugin-hongguo/pluginapi"
	"sync"
	"time"
)

type accountClient interface {
	Profile(context.Context) (account.Profile, error)
	StartLogin(context.Context) (account.Challenge, error)
	PollLogin(context.Context, string) (account.LoginStatus, error)
	CookieHeader() string
	SetCookieHeader(string)
	Favorites(context.Context, account.Device) ([]account.ShelfItem, error)
	Reservations(context.Context, account.Device, bool) ([]account.ShelfItem, error)
}

type loginAttempt struct {
	client    accountClient
	challenge account.Challenge
}
type hongguoAccount struct {
	mu         sync.Mutex
	dir        string
	client     accountClient
	attempts   map[string]*loginAttempt
	generation uint64
}

func newHongguoAccount(dir string) (*hongguoAccount, error) {
	a := &hongguoAccount{dir: dir, client: account.New(), attempts: map[string]*loginAttempt{}}
	s, ok, err := account.LoadSession(dir)
	if err != nil {
		return nil, err
	}
	if ok {
		a.client.SetCookieHeader(s.Cookie)
	}
	return a, nil
}
func (a *hongguoAccount) handle(ctx context.Context, method string, raw json.RawMessage) (value any, err error) {
	defer func() {
		if errors.Is(err, account.ErrSessionExpired) || errors.Is(err, account.ErrNeedsLogin) {
			err = pluginapi.ErrAuthRequired
		} else if errors.Is(err, account.ErrUpstream) {
			err = pluginapi.ErrUpstream
		}
	}()
	var p struct {
		Token  string
		Online *bool
	}
	if len(raw) > 0 && json.Unmarshal(raw, &p) != nil {
		return nil, pluginapi.ErrInvalidOptions
	}
	a.mu.Lock()
	client := a.client
	gen := a.generation
	a.mu.Unlock()
	switch method {
	case "AccountProfile":
		profile, err := client.Profile(ctx)
		if err != nil {
			return nil, err
		}
		profile = account.PreferAppNickname(ctx, a.dir, profile)
		a.mu.Lock()
		defer a.mu.Unlock()
		if gen != a.generation {
			return nil, context.Canceled
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err = account.SaveSession(a.dir, account.Session{Cookie: client.CookieHeader(), Profile: profile})
		return profile, err
	case "AccountLoginStart":
		client = account.New()
		ch, err := client.StartLogin(ctx)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, 24)
		if _, err = rand.Read(buf); err != nil {
			return nil, pluginapi.ErrUpstream
		}
		token := hex.EncodeToString(buf)
		a.mu.Lock()
		defer a.mu.Unlock()
		if gen != a.generation {
			return nil, context.Canceled
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// One pending login per plugin; a fresh QR invalidates the old local handle.
		a.attempts = map[string]*loginAttempt{token: {client: client, challenge: ch}}
		ch.Token = token
		return ch, nil
	case "AccountLoginWait":
		a.mu.Lock()
		attempt := a.attempts[p.Token]
		a.mu.Unlock()
		if attempt == nil {
			return account.LoginExpired, nil
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		expires := attempt.challenge.Expires
		if expires.IsZero() {
			expires = time.Now().Add(time.Minute)
		}
		for {
			if time.Now().After(expires) {
				a.mu.Lock()
				delete(a.attempts, p.Token)
				a.mu.Unlock()
				return account.LoginExpired, nil
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ticker.C:
			}
			a.mu.Lock()
			current := a.attempts[p.Token] == attempt && gen == a.generation
			a.mu.Unlock()
			if !current {
				return account.LoginExpired, nil
			}
			status, err := attempt.client.PollLogin(ctx, attempt.challenge.Token)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
			if status == account.LoginPending {
				continue
			}
			if status == account.LoginExpired {
				a.mu.Lock()
				delete(a.attempts, p.Token)
				a.mu.Unlock()
				return status, nil
			}
			profile, err := attempt.client.Profile(ctx)
			if err != nil {
				return nil, err
			}
			profile = account.PreferAppNickname(ctx, a.dir, profile)
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.attempts[p.Token] != attempt || gen != a.generation {
				return nil, context.Canceled
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err = account.SaveSession(a.dir, account.Session{Cookie: attempt.client.CookieHeader(), Profile: profile}); err != nil {
				return nil, err
			}
			a.client = attempt.client
			a.generation++
			a.attempts = map[string]*loginAttempt{}
			return account.LoginConfirmed, nil
		}
	case "AccountLogout":
		a.mu.Lock()
		defer a.mu.Unlock()
		a.generation++
		a.client = account.New()
		a.attempts = map[string]*loginAttempt{}
		return nil, account.ClearSession(a.dir)
	case "AccountFavorites":
		dev, err := account.LoadOrCreateDevice(a.dir)
		if err != nil {
			return nil, err
		}
		return client.Favorites(ctx, dev.AsDevice())
	case "AccountReservations":
		dev, err := account.LoadReservationDevice(ctx, a.dir)
		if err != nil {
			return nil, err
		}
		if p.Online != nil {
			return client.Reservations(ctx, dev, *p.Online)
		}
		upcoming, err := client.Reservations(ctx, dev, false)
		if err != nil {
			return nil, err
		}
		online, err := client.Reservations(ctx, dev, true)
		if err != nil {
			return nil, err
		}
		return append(upcoming, online...), nil
	}
	return nil, pluginapi.ErrUnsupported
}
