package account

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Profile 是账号的公开资料。
type Profile struct {
	UserID   string `json:"user_id"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
}

// profileEnvelope 兼容账号资料接口的多层结构。
type profileEnvelope struct {
	Message string `json:"message"`
	Data    struct {
		UserID    json.RawMessage `json:"user_id"`
		UserIDStr string          `json:"user_id_str"`
		Screen    string          `json:"screen_name"`
		Nickname  string          `json:"nickname"`
		Name      string          `json:"name"`
		Avatar    string          `json:"avatar_url"`
		AvatarURL string          `json:"avatar_thumb"`
		ErrorCode int             `json:"error_code"`
		Desc      string          `json:"description"`
	} `json:"data"`
}

// Profile 读取当前登录账号的资料；会话失效时返回 ErrSessionExpired。
func (c *Client) Profile(ctx context.Context) (Profile, error) {
	body, err := c.getJSON(ctx, "/passport/account/info/v2/", baseQuery())
	if err != nil {
		return Profile{}, err
	}
	var env profileEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return Profile{}, fmt.Errorf("%w: 账号资料无法解析", ErrUpstream)
	}
	if env.Data.ErrorCode == 13 || strings.Contains(env.Data.Desc, "会话过期") {
		return Profile{}, ErrSessionExpired
	}
	var result envelope
	if err := json.Unmarshal(body, &result); err != nil {
		return Profile{}, fmt.Errorf("%w: 账号资料无法解析", ErrUpstream)
	}
	if result.ErrorCode == 13 || result.Code == 13 {
		return Profile{}, ErrSessionExpired
	}
	if err := result.failed(); err != nil {
		return Profile{}, err
	}
	uid := strings.TrimSpace(env.Data.UserIDStr)
	if uid == "" {
		raw := strings.TrimSpace(string(env.Data.UserID))
		if strings.HasPrefix(raw, `"`) {
			_ = json.Unmarshal(env.Data.UserID, &uid)
		} else if raw != "" && raw != "null" {
			var number json.Number
			if json.Unmarshal(env.Data.UserID, &number) == nil {
				uid = number.String()
			}
		}
	}
	nickname := firstNonEmpty(env.Data.Nickname, env.Data.Screen, env.Data.Name)
	avatar := firstNonEmpty(env.Data.Avatar, env.Data.AvatarURL)
	uid = strings.TrimSpace(uid)
	if uid == "" || uid == "0" {
		return Profile{}, fmt.Errorf("%w: 账号资料为空", ErrUpstream)
	}
	return Profile{UserID: uid, Nickname: nickname, Avatar: avatar}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// Display 返回适合界面展示的名称。
func (p Profile) Display() string {
	if p.Nickname != "" {
		return p.Nickname
	}
	if p.UserID != "" {
		return "红果用户 " + p.UserID
	}
	return "红果账号"
}
