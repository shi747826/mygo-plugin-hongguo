# MyGo 红果插件

独立可执行插件，版本 **0.3.5**，插件 ID `hongguo`，JSONL 协议 v2。不需要 MyGo 桌面 UI 源码即可构建、测试与发布。播放依赖锁定到 openhongguo [PR #16](https://github.com/beilunyang/openhongguo/pull/16) 的提交 `3dc401af105cea273ea63f34047bc0d9e685c667`（`maddie/openhongguo v0.4.1-0.20261120080321-3dc401af105c`），在原播放接口返回 HTTP 200 空响应时增加 `video_tab` 回退。该修复尚未合并到上游正式版本，完整来源与校验和记录在 go.mod、go.sum。

## GitHub 链接安装

在支持 GitHub 安装的 MyGo Player 插件页，选择从 GitHub 安装，填写：

https://github.com/shi747826/mygo-plugin-hongguo

安装器从 release 选择与当前系统匹配的资产：**hongguo-darwin-arm64.zip**（Apple Silicon macOS）或 **hongguo-windows-amd64.zip**（Windows x64）。安装后可以在插件页点击「更新…」检查并安装新版本，更新保留启用状态、收藏、历史与插件数据。仓库为私有，安装机器应先执行 `gh auth login`，登录有仓库读取权限的 GitHub 账号，或设置 `GH_TOKEN`。

ZIP 根目录直接包含 `plugin.json` 和可执行文件（macOS 为 `plugin-hongguo`，Windows 为 `plugin-hongguo.exe`），并附许可文件；不会套额外顶层目录。macOS 可执行文件使用 ad-hoc codesign；没有 Apple 公证签名。

## 独立构建与验证

安装 Go 1.27.3 或兼容版本、macOS 命令行工具以及 zip；在本仓库执行：

```bash
go test -race ./...
go vet ./...
./scripts/build.sh
# 可指定 Go 路径：GO=/absolute/path/to/go ./scripts/build.sh
```

产物为 `dist/hongguo-<系统>-<架构>.zip`；Windows 可用 `GOOS=windows GOARCH=amd64 ./scripts/build.sh` 交叉编译。脚本自动探测本机 GOOS/GOARCH；darwin 打包须在 macOS 上执行以完成 codesign。所有构建禁用 Go workspace，不使用 sibling replace 或宿主 internal 包。

推送与清单版本一致的 `v*` tag 会触发 GitHub Actions，在 macOS 与 Linux 上分别构建 arm64 macOS 和 x64 Windows 安装 ZIP，校验清单与架构后上传同仓库 release 及 SHA256SUMS.txt。私有仓库的 release 继承私有访问权限。对应源码是该标签下完整仓库与锁定的 Go 模块依赖。

## 运行与来源

宿主以独立子进程启动插件，通过 stdin/stdout 交换协议 v2 JSONL；日志只写 stderr。源码从 MyGo Player `69c3307650bb0a58b1aa7877fec2b4c711d14b40` 拆分；本地 SDK snapshot 见 [pluginapi/SNAPSHOT.md](pluginapi/SNAPSHOT.md)。许可为 AGPL-3.0-only，来源与依赖许可见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) 与 [LICENSE](LICENSE)。

Windows 上 WebView2 默认没有 HEVC 解码器，插件会把 HEVC 片源转码为 H.264 后再交给播放器（macOS 保持原画质直接封装）。宿主设置 `MYGO_PLUGIN_DATA_DIR` 为插件独立持久化目录。账号凭据、设备、缓存与播放会话由本插件管理。需要媒体准备时调用外部 FFmpeg：设置 `MYGO_PLAYER_FFMPEG` 或安装到 PATH；安装 ZIP 不捆绑 FFmpeg。原始媒体可直接播放。离线测试默认不发真实上游请求，live/probe 测试仅显式设置对应环境变量时启用。

播放空响应先以原设备重新签名重试一次，再依次尝试 `video_tab` 和旧播放接口；成功取得模型即停止回退。插件继续设置 `RetryAttempts: 1`、`DeviceResets: 0`；只有所有回退仍返回匿名受限或设备被拒，插件层才重置一次并重新解析，最多两次调用，不无限重试。接口空响应本身不能证明设备被封禁。无需更改协议 v2、媒体准备或账号数据格式。

## Android 内嵌

`mobile/` 提供进程内适配层，供 MyGo Player 安卓包在构建时编译并注册，不启动独立进程，也不执行 FFmpeg：直链可直接播放，需要 FFmpeg 的保护流或独立音轨会明确返回不支持。播放会话随适配层关闭；与桌面插件共用来源实现、账号数据和 JSON 字段。接入方式见 [mobile/README.md](mobile/README.md)。
