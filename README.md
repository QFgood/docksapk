# 局域网服务 · DocksAPK

用 Android 手机上的一个入口打开自托管项目、查看 Ubuntu 状态和浏览事件。早期测试版包含免配对演示，安装后可直接试用。

[下载 Android 测试 APK](https://github.com/QFgood/docksapk/releases/tag/v0.1.2) · [Ubuntu 后端](https://github.com/QFgood/docksapk/releases/tag/backend-v0.1.1) · [部署和配对](docs/setup.md) · [验收记录](docs/verification.md) · [OpenAPI](api/openapi.yaml)

## 手机测试

1. 从 [v0.1.2 测试版](https://github.com/QFgood/docksapk/releases/tag/v0.1.2) 下载 `docksapk-v0.1.2-android.apk`。安装需要支持 APK 的 Android 8.0（API 26）或更高系统。
2. 打开「局域网服务」，点「体验离线演示」。点「测试项目」，提交表单并刷新。回到服务页可编辑卡片。分组中有多张卡片时，可直接调整顺序。主机页显示采样时间，断线时保留上次成功读取的状态。设置页支持发送本机测试通知，点击可打开演示事件。
3. 如需测试真实数据，按部署文档启动 Ubuntu 后端。用受信的 HTTPS 地址和一次性配对码连接。服务器上的测试项目可以接收真实的 WebView 表单请求。

v0.1.2 支持通过校验过的 JSON 清单单向导入卡片。真实 5001 格式、厂商系统推送、华为真机及家庭、校园和移动网络需要现场验收。现有事件在 App 的「通知」页展示。

## 代码结构

| 路径 | 内容 |
| --- | --- |
| `mobile/android/` | Kotlin、Jetpack Compose、Android WebView、Gradle Wrapper |
| `backend/` | Go、SQLite、Ubuntu 主机采样、Docker 只读采集、事件和 Webhook |
| `api/openapi.yaml` | API 契约 |
| `deploy/` | systemd 单元与配置示例 |
