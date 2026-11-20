# 插件发布

将工作流和 `plugin.json` 的版本更新合并，再为该提交推送一个尚未使用的 `vMAJOR.MINOR.PATCH` 标签。标签须与 `plugin.json` 的 `version` 一致。插件仓库各自维护版本，独立于宿主发布。

例如，发布 `plugin.json` 版本 `0.3.1` 时

```sh
git tag -a v0.3.1 -m 'v0.3.1'
git push origin refs/tags/v0.3.1
```

Apple Silicon runner 运行 Go 测试和 vet，并生成两个可直接在 MyGo Player 中安装的 ZIP。macOS 可执行文件使用 ad-hoc 签名。Windows x64 可执行文件由 Go 交叉编译，包里的清单指向对应的 `.exe` 文件。ZIP 保留 `LICENSE`、`THIRD_PARTY_NOTICES.md` 和 `licenses/`。

Release 资产为 `hongguo-darwin-arm64.zip`、`hongguo-windows-amd64.zip` 和 `SHA256SUMS.txt`。宿主按 ZIP 文件名中的平台和架构选择安装包。

发布使用仓库的 GitHub Actions 自动令牌。上传中断可在 **Actions → Release** 重新运行失败的任务以继续草稿发布。修正构建或源码并补发已发布版本时，应使用新的版本和标签。
