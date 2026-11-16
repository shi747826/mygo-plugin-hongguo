package account

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DeviceFile 保存本机的匿名设备标识。
//
// App 域接口按设备分配账号数据与内容（包括弹幕子集），标识每次都变会让
// 服务端当成新设备，所以必须持久化并在所有请求间复用。
type DeviceFile struct {
	DeviceID  string `json:"device_id"`
	InstallID string `json:"install_id"`
	CDID      string `json:"cdid"`
	OpenUDID  string `json:"open_udid"`
}

func devicePath(dataDir string) string {
	return filepath.Join(dataDir, "device.json")
}

// LoadOrCreateDevice 读取设备标识；不存在时生成并原子写入 0600 文件。
// 文件损坏时返回错误，不覆盖原文件。
func LoadOrCreateDevice(dataDir string) (DeviceFile, error) {
	path := devicePath(dataDir)
	data, err := os.ReadFile(path)
	if err == nil {
		var f DeviceFile
		if err := json.Unmarshal(data, &f); err != nil {
			return DeviceFile{}, fmt.Errorf("设备标识文件已损坏: %w", err)
		}
		if f.DeviceID != "" && f.InstallID != "" {
			return f, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return DeviceFile{}, fmt.Errorf("读取设备标识失败: %w", err)
	}
	f := DeviceFile{
		DeviceID:  randomDigits(19),
		InstallID: randomDigits(19),
		CDID:      randomHex(18),
		OpenUDID:  randomHex(20),
	}
	if err := writeDeviceFile(dataDir, f); err != nil {
		return DeviceFile{}, err
	}
	return f, nil
}

func writeDeviceFile(dataDir string, f DeviceFile) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("无法创建设备标识目录: %w", err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("设备标识无法序列化: %w", err)
	}
	tmp, err := os.CreateTemp(dataDir, ".device-*.tmp")
	if err != nil {
		return fmt.Errorf("无法写入设备标识: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("无法设置设备标识权限: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("无法写入设备标识: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("无法保存设备标识: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("无法保存设备标识: %w", err)
	}
	if err := os.Rename(name, devicePath(dataDir)); err != nil {
		return fmt.Errorf("无法保存设备标识: %w", err)
	}
	return nil
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%0*x", n*2, 0)
	}
	return hex.EncodeToString(buf)
}

// AsDevice 返回 App 域接口需要的两部分标识。
func (f DeviceFile) AsDevice() Device {
	return Device{DeviceID: f.DeviceID, InstallID: f.InstallID}
}
