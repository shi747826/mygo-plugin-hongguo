# 第三方与源码来源

本插件从 https://github.com/shi747826/mygo-player 的提交 `69c3307650bb0a58b1aa7877fec2b4c711d14b40` 拆分。2026-11-16 修改：独立 Go module、来源专属错误映射与测试、构建发布脚本。保留原始版权声明；整体沿用 AGPL-3.0-only（LICENSE）。协议 SDK 快照见 pluginapi/SNAPSHOT.md。未包含 MyGo UI 或桌面依赖。

- openhongguo，copyright 2026 beilunyang，Apache-2.0。当前锁定上游 PR #25 的 fork 提交 `09a0160f6f728bac624ea737eb4fdff1102e0303`，修复播放请求缺少 `x-medusa` 导致的空响应；源码：https://github.com/J3n5en/openhongguo/commit/09a0160f6f728bac624ea737eb4fdff1102e0303 ，上游修复说明：https://github.com/beilunyang/openhongguo/pull/25 。Go 模块与校验和见 go.mod、go.sum。许可：licenses/openhongguo-Apache-2.0.txt。上游 README 另有学习研究及非商业使用说明。
- 弹幕签名实现是 openhongguo v0.2.0 internal/protocol/sign 的 Apache-2.0 快照，经 MyGo Player internal/danmaku/sign 携入本仓库。原始版权与来源见 internal/danmaku/sign/NOTICE.md，许可同时保留于该目录与 licenses/danmaku-sign-Apache-2.0.txt。
- FFmpeg 作为可选外部命令调用，本仓库与 ZIP 不捆绑 FFmpeg。配置 MYGO_PLAYER_FFMPEG 或 PATH；直接媒体播放不要求安装，需准备的流要求安装。

媒体、封面与平台内容不随源码或安装包分发。完整对应源码位于同一私有仓库的版本标签，获得二进制的授权用户应同时获得该源码访问权。
