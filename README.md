# MyGo 红果插件

独立可执行插件，版本 **0.3.1**，依赖 `openhongguo v0.3.1`（含 PR #13 的 V5 播放与地址解析修复），插件 ID `hongguo`，JSONL 协议 v2。不需要 MyGo 桌面 UI 源码即可构建、测试与发布。

## GitHub 链接安装

在支持 GitHub 安装的 MyGo Player 插件页，选择从 GitHub 安装，填写：

https://github.com/shi747826/mygo-plugin-hongguo

安装器从 release 选择 `darwin-arm64` 对应资产 **hongguo-darwin-arm64.zip**。已发布的旧版本可下载 [v0.3.0 release](https://github.com/shi747826/mygo-plugin-hongguo/releases/tag/v0.3.0) 的 ZIP，在插件页从 ZIP 安装，然后启用插件。仓库为私有，安装机器应先执行 `gh auth login`，登录有仓库读取权限的 GitHub 账号。此安装包支持 Apple Silicon macOS。

ZIP 根目录直接包含 `plugin.json` 和可执行文件 `plugin-hongguo`，并附许可文件；不会套额外顶层目录。macOS 可执行文件使用 ad-hoc codesign；没有 Apple 公证签名。

## 独立构建与验证

安装 Go 1.27.3 或兼容版本、macOS 命令行工具以及 zip；在本仓库执行：

```bash
go test -race ./...
go vet ./...
./scripts/build.sh
# 可指定 Go 路径：GO=/absolute/path/to/go ./scripts/build.sh
```

产物为 `dist/hongguo-darwin-arm64.zip`。脚本自动探测本机 GOOS/GOARCH；darwin 打包须在 macOS 上执行以完成 codesign。所有构建禁用 Go workspace，不使用 sibling replace 或宿主 internal 包。

推送与清单版本一致的 `v*` tag 会触发 GitHub Actions，在 macOS 构建 arm64 安装 ZIP 并上传同仓库 release。私有仓库的 release 继承私有访问权限。对应源码是该标签下完整仓库与锁定的 Go 模块依赖。

## 运行与来源

宿主以独立子进程启动插件，通过 stdin/stdout 交换协议 v2 JSONL；日志只写 stderr。源码从 MyGo Player `69c3307650bb0a58b1aa7877fec2b4c711d14b40` 拆分；本地 SDK snapshot 见 [pluginapi/SNAPSHOT.md](pluginapi/SNAPSHOT.md)。许可为 AGPL-3.0-only，来源与依赖许可见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) 与 [LICENSE](LICENSE)。

宿主设置 `MYGO_PLUGIN_DATA_DIR` 为插件独立持久化目录。账号凭据、设备、缓存与播放会话由本插件管理。需要媒体准备时调用外部 FFmpeg：设置 `MYGO_PLAYER_FFMPEG` 或安装到 PATH；安装 ZIP 不捆绑 FFmpeg。原始媒体可直接播放。离线测试默认不发真实上游请求，live/probe 测试仅显式设置对应环境变量时启用。

`openhongguo v0.3.1` 的 V5 播放空响应会先以原设备重新签名重试一次，再回退旧播放接口。插件继续设置 `RetryAttempts: 1`、`DeviceResets: 0`；插件层仅在匿名受限或设备被拒时重置一次并重新解析，最多两次调用，不无限重试。无需更改协议 v2、媒体准备或账号数据格式。

## Android 内嵌

`mobile/` 提供进程内适配层，供 MyGo Player 安卓包在构建时编译并注册，不启动独立进程，也不执行 FFmpeg：直链可直接播放，需要 FFmpeg 的保护流或独立音轨会明确返回不支持。播放会话随适配层关闭；与桌面插件共用来源实现、账号数据和 JSON 字段。接入方式见 [mobile/README.md](mobile/README.md)。
