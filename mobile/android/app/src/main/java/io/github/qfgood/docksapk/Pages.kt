package io.github.qfgood.docksapk

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import org.json.JSONArray
import org.json.JSONObject
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

internal fun date(value: String): String = runCatching {
    DateTimeFormatter.ofPattern("MM-dd HH:mm:ss").withZone(ZoneId.systemDefault()).format(Instant.parse(value))
}.getOrDefault("—")

@Composable
internal fun CardEditor(card: ServiceCard, busy: Boolean, serverError: String, onDismiss: () -> Unit,
    onSave: (ServiceCard) -> Unit, onChangeAfterError: () -> Unit, onDelete: () -> Unit, onClear: () -> Unit) {
    var name by rememberSaveable(card.id) { mutableStateOf(card.name) }
    var group by rememberSaveable(card.id) { mutableStateOf(card.group) }
    var position by rememberSaveable(card.id) { mutableStateOf(card.position.toString()) }
    var lan by rememberSaveable(card.id) { mutableStateOf(card.lan) }
    var remote by rememberSaveable(card.id) { mutableStateOf(card.remote) }
    var icon by rememberSaveable(card.id) { mutableStateOf(card.icon) }
    var origins by rememberSaveable(card.id) { mutableStateOf(card.origins.joinToString("\n")) }
    var validation by remember { mutableStateOf("") }

    fun changed() {
        validation = ""
        onChangeAfterError()
    }

    AlertDialog(
        onDismissRequest = { if (!busy) onDismiss() },
        title = { Text(if (card.id.isBlank()) "新增卡片" else "编辑卡片") },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(name, { name = it; changed() }, Modifier.fillMaxWidth(), enabled = !busy,
                    label = { Text("名称") }, singleLine = true)
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedTextField(group, { group = it; changed() }, Modifier.weight(2f), enabled = !busy,
                        label = { Text("分组") }, singleLine = true)
                    OutlinedTextField(position, { position = it; changed() }, Modifier.weight(1f), enabled = !busy,
                        label = { Text("排序") }, singleLine = true, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number))
                }
                OutlinedTextField(lan, { lan = it; changed() }, Modifier.fillMaxWidth(), enabled = !busy,
                    label = { Text("项目 HTTPS 地址") }, singleLine = true, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri))
                OutlinedTextField(remote, { remote = it; changed() }, Modifier.fillMaxWidth(), enabled = !busy,
                    label = { Text("远程 HTTPS 地址（可选）") }, singleLine = true)
                OutlinedTextField(icon, { icon = it; changed() }, Modifier.fillMaxWidth(), enabled = !busy,
                    label = { Text("图标文字（可选）") }, singleLine = true)
                OutlinedTextField(origins, { origins = it; changed() }, Modifier.fillMaxWidth(), enabled = !busy,
                    label = { Text("额外可信 HTTPS 来源") }, supportingText = { Text("登录跳转所需，每行一个来源") },
                    maxLines = 3)
                if (card.overridden) TextButton(enabled = !busy, onClick = onClear) { Text("恢复来源值") }
                if (card.id.isNotBlank()) TextButton(enabled = !busy, onClick = onDelete) {
                    Text("移除卡片", color = MaterialTheme.colorScheme.error)
                }
                if (validation.isNotBlank() || serverError.isNotBlank()) StatusLine(validation.ifBlank { serverError }, true)
            }
        },
        confirmButton = {
            TextButton(enabled = !busy, onClick = {
                val order = position.toIntOrNull()
                val trusted = origins.lineSequence().map(String::trim).filter(String::isNotEmpty).toList()
                if (name.isBlank() || group.isBlank() || order == null || order !in 0..100000) {
                    validation = "请填写名称与分组，排序为 0 至 100000。"
                } else if (runCatching {
                    webOrigin(lan.trim())
                    if (remote.isNotBlank()) webOrigin(remote.trim())
                    trusted.forEach(::apiOrigin)
                }.isFailure) {
                    validation = "项目与可信来源必须使用有效的 HTTPS 地址。"
                } else {
                    onSave(card.copy(name = name.trim(), group = group.trim(), position = order, lan = lan.trim(),
                        remote = remote.trim(), icon = icon.trim(), origins = trusted.map(::apiOrigin).distinct()))
                }
            }) { Text("保存") }
        },
        dismissButton = { TextButton(enabled = !busy, onClick = onDismiss) { Text("取消") } },
    )
}

private val demoHost = JSONObject("""{
    "cpu_percent":12.4,"memory_used":2936012800,"memory_total":8589934592,
    "disk_used":19327352832,"disk_total":118111600640,"rx_bps":1234,"tx_bps":425,
    "uptime_seconds":192312,"docker_state":"ok",
    "containers":[{"name":"sample-service","state":"running","health":"healthy","project":"demo"}]
}""")

@Composable
internal fun HostScreen(sample: JSONObject?, demo: Boolean, online: Boolean) {
    val data = if (demo) demoHost else sample
    LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        if (demo) item { StatusLine("当前显示演示指标。") }
        else if (data?.optString("sampled_at").orEmpty().isNotBlank()) item {
            val at = data?.optString("sampled_at").orEmpty()
            val stale = runCatching { Instant.now().epochSecond - Instant.parse(at).epochSecond > 90 }.getOrDefault(true)
            StatusLine("采样 ${date(at)}${if (!online || stale) " · 数据可能过期" else ""}", !online || stale)
        }
        if (data == null || data.optString("state") == "unavailable") item { StatusLine("等待 Ubuntu 主机采集结果。") }
        else {
            item { Metric("CPU", String.format(java.util.Locale.ROOT, "%.1f%%", data.optDouble("cpu_percent", 0.0))) }
            item { Metric("内存", "${size(data.optLong("memory_used"))} / ${size(data.optLong("memory_total"))}") }
            item { Metric("磁盘", "${size(data.optLong("disk_used"))} / ${size(data.optLong("disk_total"))}") }
            item { Metric("网络", "接收 ${size(data.optLong("rx_bps"))}/秒 · 发送 ${size(data.optLong("tx_bps"))}/秒") }
            item {
                val uptime = data.optLong("uptime_seconds")
                Metric("运行时间", "${uptime / 86400} 天 ${uptime / 3600 % 24} 小时 ${uptime / 60 % 60} 分")
            }
            item {
                Text("Docker 容器", style = MaterialTheme.typography.titleLarge, fontWeight = FontWeight.Bold)
                val state = data.optString("docker_state")
                if (state != "ok") StatusLine(if (state == "disabled") "Docker 采集器尚未配置。" else "Docker 采集器暂不可用。")
                else if (data.optJSONArray("containers").objects().isEmpty()) StatusLine("暂无容器。")
            }
            items(data.optJSONArray("containers").objects()) { c ->
                val health = when (c.optString("health")) {
                    "healthy" -> "健康"
                    "unhealthy" -> "不健康"
                    "starting" -> "正在启动"
                    else -> "未配置"
                }
                Metric(c.optString("name"), listOf(c.optString("state"), health, c.optString("project"))
                    .filter(String::isNotBlank).joinToString(" · "))
            }
        }
    }
}

private fun size(bytes: Long): String {
    val units = listOf("B", "KiB", "MiB", "GiB", "TiB")
    var value = bytes.toDouble()
    var unit = 0
    while (value >= 1024 && unit < units.lastIndex) { value /= 1024; unit++ }
    return if (unit == 0) "$bytes B" else String.format(java.util.Locale.ROOT, "%.1f %s", value, units[unit])
}

@Composable
private fun Metric(label: String, value: String) {
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(3.dp)) {
            Text(label, style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Text(value, style = MaterialTheme.typography.titleMedium)
        }
    }
}

internal val demoEvent = JSONObject()
    .put("id", "demo-event").put("summary", "演示服务重新启动，运行状态已恢复。")
    .put("service", "测试项目").put("level", "info").put("source", "demo").put("shortcut_id", "demo-project")
    .put("occurred_at", Instant.now().toString()).put("read", true)

private fun label(level: String): String = when (level) {
    "info" -> "信息"
    "warning" -> "警告"
    "critical" -> "严重"
    else -> "全部"
}

@Composable
internal fun EventsScreen(events: List<JSONObject>, demo: Boolean, unread: Int, level: String, service: String,
    cursor: String, onLevel: (String) -> Unit, onService: (String) -> Unit, onFilter: () -> Unit,
    onMore: () -> Unit, onEvent: (JSONObject) -> Unit) {
    val data = if (demo) listOf(demoEvent).filter {
        (level.isEmpty() || level == it.optString("level")) &&
            (service.isBlank() || it.optString("service").contains(service.trim()))
    } else events
    LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(20.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item {
            Text(if (demo) "演示事件" else "$unread 条未读", style = MaterialTheme.typography.titleLarge)
            Row(horizontalArrangement = Arrangement.spacedBy(7.dp)) {
                listOf("", "info", "warning", "critical").forEach {
                    FilterChip(selected = level == it, onClick = { onLevel(it) }, label = { Text(label(it)) })
                }
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                OutlinedTextField(service, onService, Modifier.weight(1f), singleLine = true,
                    label = { Text("按服务名称筛选") })
                TextButton(onFilter) { Text("筛选") }
            }
        }
        if (data.isEmpty()) item { StatusLine("当前没有符合条件的事件。") }
        items(data, key = { it.optString("id") }) { item ->
            Card(onClick = { onEvent(item) }, modifier = Modifier.fillMaxWidth()) {
                Column(Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    val read = item.optBoolean("read")
                    Text("${label(item.optString("level"))} · ${item.optString("service")}${if (read) "" else " · 未读"}",
                        style = MaterialTheme.typography.labelLarge)
                    Text(item.optString("summary"), fontWeight = if (read) FontWeight.Normal else FontWeight.SemiBold)
                    Text(date(item.optString("occurred_at")), style = MaterialTheme.typography.bodySmall)
                }
            }
        }
        if (cursor.isNotBlank() && !demo) item { OutlinedButton(onMore) { Text("加载更多") } }
    }
}

@Composable
internal fun EventDetail(item: JSONObject, cards: List<ServiceCard>, onOpen: (ServiceCard) -> Unit,
    onRelated: (String) -> Unit) {
    val related = item.optString("recovery_of")
    val target = cards.find { it.id == item.optString("shortcut_id") }
    LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            Text(item.optString("service"), style = MaterialTheme.typography.headlineSmall, fontWeight = FontWeight.Bold)
            Text(item.optString("summary"), Modifier.padding(top = 8.dp), style = MaterialTheme.typography.bodyLarge)
        }
        item { Metric("级别", label(item.optString("level"))) }
        item { Metric("发生时间", date(item.optString("occurred_at"))) }
        item { Metric("来源", item.optString("source")) }
        if (target != null) item { Button(onClick = { onOpen(target) }) { Text("打开 ${target.name}") } }
        if (related.isNotBlank()) item { OutlinedButton(onClick = { onRelated(related) }) { Text("查看原告警") } }
    }
}

@Composable
internal fun SettingsScreen(session: Session?, demo: Boolean, active: String, diagnosis: String,
    settings: JSONObject?, rules: JSONObject?, busy: Boolean, onDiagnose: (String) -> Unit,
    onRules: (JSONObject) -> Unit, onTestNotice: () -> Unit, onUnbind: () -> Unit) {
    LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        if (demo) {
            item { StatusLine("演示模式可用于体验应用。连接后端后可使用设备配对和共享数据。") }
            item { Button(onTestNotice, enabled = !busy) { Text("发送本机测试通知") } }
            item { OutlinedButton(onUnbind) { Text("退出演示") } }
        } else if (session != null) {
            item { Metric("当前实例", session.name) }
            item { Metric("设备", session.device) }
            item { Metric("当前连接", active.ifBlank { session.address }) }
            val addresses = listOf("配对入口" to session.address, "局域网入口" to session.lan, "远程入口" to session.remote)
            items(addresses) { (name, url) ->
                if (url.isNotBlank()) {
                    Metric(name, url)
                    TextButton(enabled = !busy, onClick = { onDiagnose(url) }) { Text("诊断$name") }
                }
            }
            if (diagnosis.isNotBlank()) item { StatusLine(diagnosis) }
            item {
                Metric("系统推送", if (settings?.optString("push_status") == "unconfigured")
                    "尚未接入厂商推送，事件可在通知页查看。" else "等待后端配置。")
            }
            item { Button(onTestNotice, enabled = !busy) { Text("发送本机测试通知") } }
            if (rules != null) item {
                val enabled = rules.optBoolean("enabled")
                val levels = rules.optJSONArray("levels").strings()
                val service = rules.optBoolean("show_service_name")
                fun save(active: Boolean, selected: List<String>, show: Boolean) {
                    onRules(JSONObject().put("enabled", active).put("levels", JSONArray(selected)).put("show_service_name", show))
                }
                Text("通知订阅", style = MaterialTheme.typography.titleLarge)
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("启用订阅", Modifier.weight(1f))
                    Switch(enabled, { save(it, levels, service) }, enabled = !busy)
                }
                for (level in listOf("info", "warning", "critical")) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(label(level), Modifier.weight(1f))
                        Checkbox(level in levels, { save(enabled, if (it) levels + level else levels - level, service) },
                            enabled = !busy && enabled)
                    }
                }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("在推送中显示服务名", Modifier.weight(1f))
                    Switch(service, { save(enabled, levels, it) }, enabled = !busy && enabled)
                }
            }
            item { OutlinedButton(onUnbind, enabled = !busy) { Text("解绑设备") } }
        }
    }
}
