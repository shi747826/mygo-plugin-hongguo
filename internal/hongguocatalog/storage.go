package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const historyLimit = 200

func emptyLibrary() Library {
	return Library{Favorites: map[string]Series{}, History: map[string]Progress{}}
}
func cloneLibrary(in Library) Library {
	out := emptyLibrary()
	for k, v := range in.Favorites {
		out.Favorites[k] = v
	}
	for k, v := range in.History {
		out.History[k] = v
	}
	return out
}

func (c *Client) open() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return localError("无法创建数据目录，请检查目录权限", err)
	}
	if err := rejectSymlink(c.path); err != nil {
		return localError("无法读取收藏历史，请使用普通文件", err)
	}
	data, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		c.library = emptyLibrary()
		return c.persist(c.library)
	}
	if err != nil {
		return localError("收藏历史读取失败，请检查文件权限", err)
	}
	var lib *Library
	if err := json.Unmarshal(data, &lib); err != nil {
		return localError("收藏历史文件损坏，请备份 library.json 后修复 JSON", err)
	}
	if lib == nil {
		return localError("收藏历史文件损坏，请备份 library.json 后检查内容", errors.New("empty library"))
	}
	for key, s := range lib.Favorites {
		if key != s.ID || strings.TrimSpace(s.ID) == "" {
			return localError("收藏历史文件内容无效，请备份后检查作品编号", errors.New("invalid favorite identity"))
		}
	}
	for key, p := range lib.History {
		if key != p.Series.ID || validateProgress(p) != nil || p.UpdatedAt.IsZero() {
			return localError("观看历史内容无效，请备份后检查作品、分集与进度", errors.New("invalid history entry"))
		}
	}
	c.library = cloneLibrary(*lib)
	pruneHistory(c.library.History)
	// 旧文件也收紧权限；裁剪超量历史一并持久化。
	return c.persist(c.library)
}

// Library 返回独立快照，调用方修改 map 不影响客户端。
func (c *Client) Library() (Library, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneLibrary(c.library), nil
}

// ToggleFavorite 成功时返回切换后的收藏状态。失败时保持原状态。
func (c *Client) ToggleFavorite(series Series) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, exists := c.library.Favorites[series.ID]
	if strings.TrimSpace(series.ID) == "" {
		return exists, errors.New("收藏失败，请先选择有效的短剧")
	}
	next := cloneLibrary(c.library)
	if exists {
		delete(next.Favorites, series.ID)
	} else {
		next.Favorites[series.ID] = series
	}
	if err := c.persist(next); err != nil {
		return exists, err
	}
	c.library = next
	return !exists, nil
}

// MergeFavorites 把一批短剧并入本地收藏，已存在的不重复添加。
// 返回新增数量；持久化失败时本地状态不变。
func (c *Client) MergeFavorites(series []Series) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := cloneLibrary(c.library)
	added := 0
	for _, s := range series {
		if strings.TrimSpace(s.ID) == "" {
			continue
		}
		if _, exists := next.Favorites[s.ID]; exists {
			continue
		}
		next.Favorites[s.ID] = s
		added++
	}
	if added == 0 {
		return 0, nil
	}
	if err := c.persist(next); err != nil {
		return 0, err
	}
	c.library = next
	return added, nil
}

func validateProgress(p Progress) error {
	if strings.TrimSpace(p.Series.ID) == "" || strings.TrimSpace(p.Episode.ID) == "" {
		return errors.New("保存进度失败，请先选择短剧和分集")
	}
	if p.Episode.Number < 1 {
		return errors.New("保存进度失败，分集编号必须从 1 开始")
	}
	if p.Position < 0 || math.IsNaN(p.Position) || math.IsInf(p.Position, 0) {
		return errors.New("保存进度失败，播放位置必须是有效的非负秒数")
	}
	return nil
}

func (c *Client) SaveProgress(p Progress) error {
	if err := validateProgress(p); err != nil {
		return err
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// 忽略延迟到达的旧记录，防止异步保存倒退。
	if old, ok := c.library.History[p.Series.ID]; ok && old.UpdatedAt.After(p.UpdatedAt) {
		return nil
	}
	next := cloneLibrary(c.library)
	next.History[p.Series.ID] = p
	pruneHistory(next.History)
	if err := c.persist(next); err != nil {
		return err
	}
	c.library = next
	return nil
}

func pruneHistory(history map[string]Progress) {
	if len(history) <= historyLimit {
		return
	}
	keys := make([]string, 0, len(history))
	for key := range history {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := history[keys[i]].UpdatedAt, history[keys[j]].UpdatedAt
		if a.Equal(b) {
			return keys[i] < keys[j]
		}
		return a.After(b)
	})
	for _, key := range keys[historyLimit:] {
		delete(history, key)
	}
}

func (c *Client) persist(lib Library) error {
	if err := atomicJSON(c.path, lib); err != nil {
		return localError("保存收藏历史失败，请检查数据目录权限与可用空间后重试", err)
	}
	return nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("data file is not a regular file")
	}
	return nil
}
func privateExisting(path string) error {
	if err := rejectSymlink(path); err != nil {
		return err
	}
	err := os.Chmod(path, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func atomicJSON(path string, value any) error {
	if err := rejectSymlink(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".catalog-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// CreateTemp 在目标目录创建且返回真实路径；仅清理由本次调用创建的文件。
	defer os.Remove(name)
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
