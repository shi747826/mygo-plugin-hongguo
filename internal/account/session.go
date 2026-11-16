package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxSessionBytes 限制本地会话文件大小，避免读取异常文件。
const maxSessionBytes = 64 << 10

// Session 是保存在本机的登录态。只保存 Cookie 与昵称，不保存二维码 token。
type Session struct {
	Cookie  string    `json:"cookie"`
	Profile Profile   `json:"profile"`
	SavedAt time.Time `json:"saved_at"`
}

// SessionPath 返回会话文件路径。
func SessionPath(dataDir string) string {
	return filepath.Join(dataDir, "account.json")
}

// LoadSession 读取已保存的登录态；文件不存在时返回 (Session{}, false, nil)。
func LoadSession(dataDir string) (Session, bool, error) {
	path := SessionPath(dataDir)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("读取登录状态失败: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxSessionBytes {
		return Session{}, false, errors.New("登录状态必须是大小受限的普通文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return Session{}, false, fmt.Errorf("读取登录状态失败: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return Session{}, false, errors.New("登录状态文件已变化")
	}
	if perm := opened.Mode().Perm(); perm&0o077 != 0 {
		if err := f.Chmod(0o600); err != nil {
			return Session{}, false, fmt.Errorf("登录状态文件权限过宽: %w", err)
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSessionBytes+1))
	if err != nil {
		return Session{}, false, fmt.Errorf("读取登录状态失败: %w", err)
	}
	if len(data) > maxSessionBytes {
		return Session{}, false, errors.New("登录状态文件过大")
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return Session{}, false, fmt.Errorf("登录状态文件已损坏: %w", err)
	}
	if !validSession(s) {
		return Session{}, false, nil
	}
	return s, true, nil
}

// validSession 判断会话是否具备可用的身份信息。
func validSession(s Session) bool {
	if strings.TrimSpace(s.Cookie) == "" {
		return false
	}
	return strings.TrimSpace(s.Profile.UserID) != "" || strings.TrimSpace(s.Profile.Nickname) != ""
}

// SaveSession 原子写入登录态，文件权限 0600。
func SaveSession(dataDir string, s Session) error {
	if !validSession(s) {
		return errors.New("登录状态缺少有效 Cookie 或账号身份")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("无法创建数据目录: %w", err)
	}
	s.SavedAt = time.Now()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("登录状态无法序列化: %w", err)
	}
	path := SessionPath(dataDir)
	tmp, err := os.CreateTemp(dataDir, ".account-*.tmp")
	if err != nil {
		return fmt.Errorf("无法写入登录状态: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("无法设置登录状态权限: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("无法写入登录状态: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("无法保存登录状态: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("无法保存登录状态: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("无法保存登录状态: %w", err)
	}
	return nil
}

// ClearSession 删除本机登录态。
func ClearSession(dataDir string) error {
	err := os.Remove(SessionPath(dataDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("无法清除登录状态: %w", err)
	}
	return nil
}
