# 部署与升级

## 安装 Ubuntu 后端

发布页提供 Linux amd64 和 arm64 二进制。先运行 `uname -m` 选择架构，下载对应归档后用同一发布页的 `SHA256SUMS.txt` 验证。以下命令在解压目录执行。

```sh
sudo groupadd --system docks
sudo useradd --system --gid docks --shell /usr/sbin/nologin docks
sudo useradd --system --gid docks --shell /usr/sbin/nologin docks-collector
sudo install -m 0755 docksd docks-collector /usr/local/bin/
sudo install -m 0644 systemd/*.service /etc/systemd/system/
sudo install -d -m 0750 /etc/docksapk
sudo install -m 0600 docksd.env.example /etc/docksapk/docksd.env
sudoedit /etc/docksapk/docksd.env
sudo systemctl daemon-reload
sudo systemctl enable --now docksd.service
curl -fsS http://127.0.0.1:8080/healthz
```

`docksd` 监听回环地址。用现有受信 HTTPS 反向代理转发到 `http://127.0.0.1:8080`，只通过家庭网络或 tailnet 暴露入口。可按网络使用不同域名，也可共用一个受信域名。例如已登录 Tailscale 的主机可运行 `tailscale serve --bg http://127.0.0.1:8080`，将返回的 HTTPS 域名填入 `--remote`。同时确保手机实际可达该入口和各项目网页。若直接由 Go 提供 TLS，用 `--tls-cert` 和 `--tls-key` 指定证书链和私钥，系统需要信任证书签发链，域名必须匹配。项目网页也需提供 HTTPS。

采集器服务以独立用户运行，只有它加入 Docker 高权限组。采集器通过权限为 0660 的本地 socket 提供容器状态及事件，无写操作。没有 Docker 时主机指标仍可使用，App 会显示 Docker 采集器不可用。可临时不使用 systemd 运行 `docksd serve --db ./docks.db` 来启动仅在回环监听的后端。

## 配对与测试项目

```sh
sudo -u docks docksd pair-code --db /var/lib/docksapk/docks.db
sudo -u docks docksd devices --db /var/lib/docksapk/docks.db
sudo -u docks docksd revoke --db /var/lib/docksapk/docks.db --device DEVICE_ID
```

第一次连接时输入 HTTPS 原点（无路径）和配对码。每个配对码有效 5 分钟，只能兑换一次。连接的手机可在设置中查看设备，诊断后端 TLS 与实例标识，或解绑设备。配对后端记录受信备用地址，App 仅在 TLS 及实例校验成功后发送 API 凭证。读请求允许尝试备用地址，写请求由用户在恢复连接后重新提交。

要用真实后端测试 WebView 表单，在 `DOCKS_API_ARGS` 的值中加入 `--demo`，然后重启服务，并为测试页添加卡片。

```sh
sudo systemctl restart docksd
sudo -u docks docksd seed-demo --db /var/lib/docksapk/docks.db --base https://hub.example.net
```

App 刷新后打开「测试项目」，输入测试内容并提交。网页应显示服务端回执，再次打开应显示项目 Cookie 已保留。该测试页无需项目账号，只应在测试时启用。点项目页「更多」可切换登记的项目地址、复制地址或用浏览器打开，浏览器和 App 各自管理 Cookie。

## 导入导航与接收 Webhook

```sh
sudo -u docks docksd import --db /var/lib/docksapk/docks.db --source navigation --file /path/readable-by-docks.json
```

参考 `navigation.example.json`。每项需要稳定的 `source_id`，重命名时保持这个字段。只在确认文件含全量数据后填入 `"complete": true`。一次完整成功导入会隐藏缺项，失败会保留上次的数据。编辑导入卡片会记录单项字段覆盖，可在 App 中清除。此版本需要把实际导航数据转换成此格式后再导入。

在 `docksd.env` 的 `DOCKS_WEBHOOK_SECRETS` 中登记自定义来源及密钥，重启服务。示例来源 `example` 的请求为：

```sh
curl --fail-with-body https://hub.example.net/v1/webhooks/example \
  -H "Authorization: Bearer $DOCKS_EXAMPLE_WEBHOOK_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"source_event_id":"alarm-001","service":"项目示例","level":"warning","summary":"测试告警"}'
```

应用可另传 `shortcut_id` 将事件关联到卡片，或用 `recovery_of` 关联同一来源和服务的原事件。只提交可展示的摘要。Webhook 重复投递会返回同一条事件；已读状态在同实例各设备同步。主机每 30 秒采样，详细数据保留 24 小时、5 分钟聚合保留 7 天，事件默认保留 30 天。Docker 采集短时断开后尝试在有限的缓冲中补录，可能不完整时会生成缺口事件。

Docker 健康检查只记录状态变化。同一容器恢复健康时，恢复事件会关联最近一次未恢复的告警。相同容器的相同异常退出或 OOM 告警在 5 分钟内只记录一次。每批 Docker 事件和采集游标同时提交，写入失败时会保留上次游标以便重试。

## 将 Docker 事件关联到项目卡片

先创建或导入项目卡片，再用本地命令查询卡片 ID。

```sh
sudo -u docks docksd shortcuts --db /var/lib/docksapk/docks.db
```

命令按「ID、分组、名称」列出可见卡片。在 `/etc/docksapk/docksd.env` 中配置服务名与卡片 ID 的映射，随后重启 `docksd`。键取通知页中的 Docker 服务名，区分大小写。有 Compose 项目标签时是项目名，否则是容器名。将下方示例中的服务名和 ID 换成自己的值。

```sh
DOCKS_DOCKER_SHORTCUTS='{"media":"0123456789abcdef0123456789abcdef"}'
```

读取事件时按当前映射查找可见卡片，因此历史事件也可打开关联项目。隐藏或删除卡片后，该项目入口会从 Docker 事件详情消失。Webhook 的显式 `shortcut_id` 仍按原方式解析。

## 构建与升级

Android 使用 JDK 17、Gradle Wrapper 8.10.2、AGP 8.7.3、SDK 35。安装 SDK Platform 35 和 Build Tools 34.0.0（AGP 默认）后执行：

```sh
cd mobile/android
./gradlew :app:assembleDebug
# app/build/outputs/apk/debug/app-debug.apk
./gradlew :app:lintDebug
./gradlew :app:assembleRelease
# app/build/outputs/apk/release/app-release-unsigned.apk
```

发布测试包用 Android SDK Build Tools 的 `zipalign` 和 `apksigner` 对未签名包签名。之后覆盖安装需要沿用相同应用 ID、签名密钥和更高的版本号。保管签名密钥和密码，勿提交到 Git。调试包的签名通常与发布测试包不同。

后端在 Linux 或面向 Linux 交叉编译。使用 Go 1.23 或更高版本。

```sh
cd backend
go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o docksd ./cmd/docksd
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o docks-collector ./cmd/docks-collector
```

## 数据库备份与恢复

升级前停止 `docksd`，用 SQLite 的 `.backup` 命令生成一致备份，并保留原二进制和配置。把以下命令中的备份路径替换成服务用户可写的新文件。后端运行时也可以通过 SQLite 在线备份接口生成一致快照。

```sh
sudo -u docks sqlite3 /var/lib/docksapk/docks.db '.backup /path/to/protected/docks.db'
sudo -u docks sqlite3 /path/to/protected/docks.db 'PRAGMA quick_check;'
```

升级后先检查 `/healthz`，再用已有配对设备核对卡片和事件历史。恢复或回滚时保持服务停止，把当前数据库及可能存在的同名 `-wal` 和 `-shm` 文件移到受保护的目录，再将备份复制到原数据库路径。将恢复后的数据库归属设为 `docks:docks`，文件权限设为 `0600`。回滚时使用与备份匹配的旧二进制和配置。

数据恢复到备份时间点。对备份之后撤销的设备，恢复后、启动服务前应再次用 `docksd revoke` 撤销。数据库和备份含设备凭证摘要与内网服务地址，应限制读权限。
