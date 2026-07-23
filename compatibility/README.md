# Compatibility Manifests

本目录只存放经过测试的精确宿主版本兼容清单。设计期没有任何版本被自动认证。

`v0.1.0-alpha.1` 发布时没有任何官方 New API revision 获得 Certified
标记。当前 Bridge 代码来自一个以 `v1.0.0-rc.20` 为基础、HEAD 为
`6ce7305cd36f16506fb6a2c3c524a5a318539ba7` 的本地参考 fork；官方 rc.20
tag commit 是 `a7f3067bf34a2fa125f843acdfcf45d0b0bfd682`，两者不能混同。
2026-07-23 联网审计观察到最新 Release 为 `v1.0.0-rc.21`，它也尚未认证。

一个兼容条目必须固定：

- New API upstream repository identity；
- exact commit 和可选 tag；
- source tree fingerprint；
- Bridge/Core/Schema/Policy/Receipt 版本；
- 必需 Hook；
- 数据库版本；
- build toolchain；
- image digest；
- Parity Manifest 路径和 digest；
- 已知缺口；
- release/expiry/revocation 状态。

禁止：

- 用 `main`、`master` 或 `latest` 作为认证目标；
- 用 semver 范围暗示未测试 commit 兼容；
- compatibility 过期后继续显示 Certified；
- 从当前内部脏工作树生成公开兼容记录；
- 只凭编译成功填 `full_parity`。

未来文件命名建议：

```text
new-api-<tag>-<short-commit>.yaml
```

每个条目引用符合 `parts/spec/parity-manifest.schema.json` 的证据。公开首发前，本目录保持只有说明文件是正确状态。
