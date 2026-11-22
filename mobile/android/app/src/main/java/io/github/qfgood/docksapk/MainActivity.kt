package io.github.qfgood.docksapk

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import org.json.JSONArray
import org.json.JSONObject
import java.net.URLEncoder
import java.util.UUID

private val spruce = Color(0xFF12493E)
private val mint = Color(0xFFD9EEE4)
private val palette = lightColorScheme(
    primary = spruce, onPrimary = Color.White, primaryContainer = mint,
    onPrimaryContainer = Color(0xFF12382F), secondary = Color(0xFF486859),
    secondaryContainer = Color(0xFFDCE9E0), surface = Color(0xFFF6F8F6),
    background = Color(0xFFF6F8F6), onSurface = Color(0xFF172B24),
    onSurfaceVariant = Color(0xFF476057), outline = Color(0xFF798F84),
)

private const val localDemo = "https://appassets.androidplatform.net/assets/demo.html"
private val demoCards = listOf(ServiceCard(id = "demo-project", name = "测试项目",
    group = "演示服务", icon = "表", lan = localDemo))

class MainActivity : ComponentActivity() {
    private var notificationRoute by mutableStateOf<NoticeRoute?>(null)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        notificationRoute = NoticeRoute.from(intent)
        val store = LocalStore(this)
        setContent {
            MaterialTheme(colorScheme = palette, shapes = Shapes(
                small = RoundedCornerShape(10.dp), medium = RoundedCornerShape(16.dp), large = RoundedCornerShape(24.dp),
            )) {
                Surface(Modifier.fillMaxSize()) {
                    Hub(store, notificationRoute, onRouteHandled = { notificationRoute = null })
                }
            }
        }
    }
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        notificationRoute = NoticeRoute.from(intent)
    }
}

private enum class Tab(val title: String, val symbol: String) {
    Services("服务", "▦"), Host("主机", "▤"), Events("通知", "◉"), Settings("设置", "⚙")
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable

private fun Hub(store: LocalStore, incoming: NoticeRoute?, onRouteHandled: () -> Unit) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var session by remember { mutableStateOf(store.session()) }
    var demo by rememberSaveable { mutableStateOf(false) }
    val api = remember(session?.id, session?.address) { HubApi(session?.address.orEmpty()) }
    var tab by rememberSaveable { mutableStateOf(Tab.Services) }
    var cards by remember { mutableStateOf(emptyList<ServiceCard>()) }
    var etag by remember { mutableStateOf<String?>(null) }
    var importNote by remember { mutableStateOf("") }
    var host by remember { mutableStateOf<JSONObject?>(null) }
    var events by remember { mutableStateOf(emptyList<JSONObject>()) }
    var detail by remember { mutableStateOf<JSONObject?>(null) }
    var eventCursor by remember { mutableStateOf("") }
    var unread by remember { mutableIntStateOf(0) }
    var level by rememberSaveable { mutableStateOf("") }
    var serviceFilter by rememberSaveable { mutableStateOf("") }
    var settings by remember { mutableStateOf<JSONObject?>(null) }
    var rules by remember { mutableStateOf<JSONObject?>(null) }
    var rulesEtag by remember { mutableStateOf<String?>(null) }
    var page by remember { mutableStateOf<ServiceCard?>(null) }
    var edit by remember { mutableStateOf<ServiceCard?>(null) }
    var delete by remember { mutableStateOf<ServiceCard?>(null) }
    var orderGroup by remember { mutableStateOf<String?>(null) }
    var unbind by rememberSaveable { mutableStateOf(false) }
    var message by remember { mutableStateOf("") }
    var diagnostic by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var online by remember { mutableStateOf(false) }
    var addressUsed by remember { mutableStateOf("") }
    var pairAddress by rememberSaveable { mutableStateOf("") }
    var pairCode by remember { mutableStateOf("") }
    var pendingNotice by remember { mutableStateOf<NoticeRoute?>(null) }
    val noticePermission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        val posted = granted && pendingNotice?.let { LocalNotice.show(context, it) } == true
        message = when {
            posted -> "已发送本机测试通知。"
            granted -> "测试通知渠道已关闭。"
            else -> "系统通知权限未开启。"
        }
        pendingNotice = null
    }

    fun postNotice(route: NoticeRoute) {
        if (Build.VERSION.SDK_INT >= 33 && context.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            pendingNotice = route
            noticePermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        } else {
            message = if (LocalNotice.show(context, route)) "已发送本机测试通知。" else "系统通知或测试通知渠道已关闭。"
        }
    }

    fun consumeCards(data: JSONObject) {
        cards = data.optJSONArray("items").objects().map(ServiceCard::from)
        importNote = data.optJSONArray("imports").objects().joinToString("\n") {
            val source = it.optString("source")
            val last = date(it.optString("last_success"))
            val error = it.optString("error")
            if (error.isBlank()) "已导入 $source · $last" else "$source 上次成功 $last · $error"
        }
    }

    suspend fun loadCards(bound: Session, conditional: Boolean = true) {
        val known = if (conditional) etag else null
        val request = api.read(bound, "/v1/shortcuts", known?.let { mapOf("If-None-Match" to it) }.orEmpty())
        if (request.status != 304) {
            consumeCards(request.data)
            etag = request.etag
            store.cache(bound.id, request.data)
        }
    }

    suspend fun loadEvents(bound: Session, more: Boolean = false) {
        val qs = buildList {
            if (level.isNotEmpty()) add("level=$level")
            if (serviceFilter.isNotBlank()) add("service=${URLEncoder.encode(serviceFilter.trim(), "UTF-8")}")
            if (more && eventCursor.isNotBlank()) add("cursor=$eventCursor")
        }.joinToString("&")
        val data = api.read(bound, "/v1/events" + if (qs.isBlank()) "" else "?$qs").data
        val items = data.optJSONArray("items").objects()
        events = if (more) (events + items).distinctBy { it.getString("id") } else items
        unread = data.optInt("unread")
        eventCursor = data.optString("next_cursor")
    }

    suspend fun openEvent(bound: Session, id: String) {
        val event = api.read(bound, "/v1/events/$id").data
        detail = event
        api.write(bound, "/v1/events/$id/read", "PUT")
        val shortcut = event.optString("shortcut_id")
        if (shortcut.isNotBlank() && cards.none { it.id == shortcut }) {
            loadCards(bound)
        }
        loadEvents(bound)
    }

    suspend fun loadSettings(bound: Session) {
        val result = api.read(bound, "/v1/settings").data
        val updated = bound.copy(
            lan = runCatching { apiOrigin(result.optString("lan_url")) }.getOrDefault(""),
            remote = runCatching { apiOrigin(result.optString("tailnet_url")) }.getOrDefault(""),
        )
        if (updated != session) { store.save(updated); session = updated }
        settings = result
    }

    suspend fun refresh(bound: Session, target: Tab) {
        when (target) {
            Tab.Services -> loadCards(bound)
            Tab.Host -> {
                val sample = api.read(bound, "/v1/hosts/local/status").data
                if (sample.optString("sampled_at").isNotBlank()) {
                    store.saveHost(bound.id, sample)
                    host = sample
                } else if (host == null) {
                    host = sample
                }
            }
            Tab.Events -> loadEvents(bound)
            Tab.Settings -> {
                loadSettings(bound)
                val r = api.read(session ?: bound, "/v1/notification-rules")
                rules = r.data
                rulesEtag = r.etag
            }
        }
        addressUsed = api.active
        online = true
    }

    fun reset() {
        LocalNotice.clear(context)
        store.clear()
        session = null
        demo = false
        cards = emptyList()
        events = emptyList()
        host = null
        detail = null
        edit = null
        delete = null
        orderGroup = null
        unbind = false
        page = null
        rules = null
        settings = null
        importNote = ""
        eventCursor = ""
        unread = 0
        online = false
        tab = Tab.Services
        diagnostic = ""
    }

    fun runTask(affectsConnection: Boolean = true, block: suspend () -> Unit) {
        if (busy) return
        busy = true
        message = ""
        scope.launch {
            try {
                block()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                message = explain(e)
                if (affectsConnection && e !is ApiError) online = false
                if (affectsConnection && e is ApiError && e.status == 401) {
                    if (e.code == "invalid_device" && session != null) reset() else online = false
                }
                if (e is ApiError && e.status == 412) {
                    session?.let {
                        try {
                            refresh(it, tab)
                        } catch (cancelled: CancellationException) {
                            throw cancelled
                        } catch (refreshError: Exception) {
                            online = false
                            if (refreshError is ApiError && refreshError.status == 401 && refreshError.code == "invalid_device") {
                                reset()
                            }
                            message += " ${explain(refreshError)}"
                        }
                    }
                    orderGroup = null
                    edit = null
                    delete = null
                }
            } finally {
                busy = false
            }
        }
    }

    LaunchedEffect(session?.id, demo) {
        val bound = session
        if (bound == null) {
            cards = if (demo) (store.cache("demo")?.optJSONArray("items").objects()
                .map(ServiceCard::from).takeIf { store.cache("demo") != null } ?: demoCards) else emptyList()
        } else {
            etag = null
            store.cache(bound.id)?.let(::consumeCards)
            host = store.cachedHost(bound.id)
            runTask {
                refresh(bound, tab)
                if (tab != Tab.Settings) {
                    try {
                        loadSettings(bound)
                    } catch (cancelled: CancellationException) {
                        throw cancelled
                    } catch (error: ApiError) {
                        if (error.status == 401 && error.code == "invalid_device") throw error
                    } catch (_: java.io.IOException) {
                        // The main page already loaded. Settings can be refreshed separately.
                    }
                }
            }
        }
    }

    LaunchedEffect(incoming, busy) {
        val route = incoming ?: return@LaunchedEffect
        if (busy) return@LaunchedEffect
        onRouteHandled()
        page = null
        edit = null
        delete = null
        orderGroup = null
        tab = Tab.Events
        val bound = session
        when {
            route == NoticeRoute.demo && bound == null -> {
                demo = true
                detail = demoEvent
            }
            bound != null && route.instance == bound.id -> runTask {
                openEvent(bound, route.event)
            }
            else -> message = "此通知对应的实例未绑定。"
        }
    }

    fun visitTab(target: Tab) {
        tab = target
        message = ""
        diagnostic = ""
        if (!demo) session?.let { bound -> runTask { refresh(bound, target) } }
    }

    fun saveDemo(items: List<ServiceCard>) {
        cards = items.sortedWith(compareBy({ it.group }, { it.position }, { it.name }))
        store.cache("demo", JSONObject().put("items", JSONArray(cards.map(ServiceCard::json))))
    }

    val openCard = page
    if (openCard != null) {
        val alternate = !demo && session?.remote?.isNotBlank() == true &&
            api.active == session?.remote && session?.remote != session?.lan
        ProjectPage(openCard, alternate, localDemo, onClose = { page = null })
        return
    }

    BackHandler(detail != null || demo) {
        if (detail != null) detail = null else { demo = false; tab = Tab.Services }
    }

    if (session == null && !demo) {
        PairScreen(pairAddress, pairCode, busy, message, { pairAddress = it }, { pairCode = it }, onPair = {
            runTask(false) {
                val origin = apiOrigin(pairAddress)
                val paired = HubApi(origin).pair(pairCode)
                store.save(paired)
                pairCode = ""
                tab = Tab.Services
                session = paired
            }
        }, onDemo = {
            demo = true
            message = ""
            tab = Tab.Services
        })
        return
    }

    val bound = session
    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Column {
                        Text(if (detail == null) tab.title else "事件详情", fontWeight = FontWeight.Bold)
                        Text(if (demo) "离线演示" else bound?.name.orEmpty(),
                            style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
                            maxLines = 1, overflow = TextOverflow.Ellipsis)
                    }
                },
                navigationIcon = { if (detail != null) TextButton(onClick = { detail = null }) { Text("返回") } },
                actions = { if (!demo && detail == null) TextButton(enabled = !busy, onClick = {
                    bound?.let { runTask { refresh(it, tab) } }
                }) { Text("刷新") } },
            )
        },
        bottomBar = {
            if (detail == null) NavigationBar(containerColor = MaterialTheme.colorScheme.surfaceContainer) {
                Tab.entries.forEach { item ->
                    NavigationBarItem(
                        selected = tab == item, onClick = { if (!busy || demo) visitTab(item) },
                        icon = { Text(item.symbol, style = MaterialTheme.typography.titleLarge) },
                        label = { Text(if (item == Tab.Events && unread > 0) "${item.title} $unread" else item.title) },
                    )
                }
            }
        },
        floatingActionButton = {
            if (tab == Tab.Services && detail == null && (demo || online) && !busy) {
                ExtendedFloatingActionButton(text = { Text("新增卡片") }, icon = { Text("+") }, onClick = {
                    edit = ServiceCard(position = (cards.maxOfOrNull { it.position } ?: -1).plus(1).coerceAtMost(100000))
                })
            }
        },
    ) { padding ->
        Column(Modifier.fillMaxSize().padding(padding)) {
            if (busy) LinearProgressIndicator(Modifier.fillMaxWidth())
            if (message.isNotBlank()) StatusLine(message, true, Modifier.padding(horizontal = 20.dp, vertical = 8.dp))
            val selectedEvent = detail
            if (selectedEvent != null) {
                EventDetail(selectedEvent, cards, onOpen = { page = it }, onRelated = { id ->
                    bound?.let { runTask {
                        openEvent(it, id)
                    } }
                })
            } else when (tab) {
                Tab.Services -> Services(cards, demo, online, busy, importNote, { page = it }, { edit = it }, {
                    message = ""
                    orderGroup = it
                })
                Tab.Host -> HostScreen(host, demo, online)
                Tab.Events -> EventsScreen(events, demo, unread, level, serviceFilter, eventCursor,
                    onLevel = { level = it; bound?.let { current -> runTask { loadEvents(current) } } },
                    onService = { serviceFilter = it },
                    onFilter = { bound?.let { runTask { loadEvents(it) } } },
                    onMore = { bound?.let { runTask { loadEvents(it, true) } } },
                    onEvent = { item ->
                        if (demo) detail = item else bound?.let { runTask {
                            openEvent(it, item.getString("id"))
                        } }
                    },
                )
                Tab.Settings -> SettingsScreen(bound, demo, addressUsed, diagnostic, settings, rules, busy,
                    onDiagnose = { address ->
                        bound?.let { runTask(false) {
                            diagnostic = try {
                                api.diagnose(it, address)
                                "$address · TLS 与绑定实例验证通过"
                            } catch (e: CancellationException) { throw e }
                            catch (e: Exception) { "$address · ${explain(e)}" }
                        } }
                    },
                    onRules = { input ->
                        bound?.let { current -> runTask {
                            val result = api.write(current, "/v1/notification-rules", "PUT", input,
                                mapOf("If-Match" to (rulesEtag ?: return@runTask)))
                            rules = result.data; rulesEtag = result.etag
                        } }
                    },
                    onTestNotice = {
                        if (demo) postNotice(NoticeRoute.demo)
                        else bound?.let { current -> runTask {
                            val latest = api.read(current, "/v1/events?limit=1").data.optJSONArray("items").objects().firstOrNull()
                            if (latest == null) message = "暂无事件可用于测试通知。"
                            else postNotice(NoticeRoute(current.id, latest.getString("id")))
                        } }
                    },
                    onUnbind = { if (demo) { demo = false; tab = Tab.Services } else unbind = true },
                )
            }
        }
    }

    orderGroup?.let { group ->
        val entries = cards.filter { it.group == group }
        if (entries.size > 1) CardOrderDialog(group, entries, busy, message, onDismiss = {
            orderGroup = null
            message = ""
        }, onSave = { ordered ->
            if (demo) {
                saveDemo(cards.filterNot { it.group == group } + ordered.mapIndexed { index, card ->
                    card.copy(position = index)
                })
                orderGroup = null
            } else bound?.let { current ->
                runTask {
                    val payload = JSONObject().put("group_id", group).put("ids", JSONArray(ordered.map { it.id }))
                    api.write(current, "/v1/shortcut-order", "PUT", payload,
                        mapOf("If-Match" to requireNotNull(etag)))
                    orderGroup = null
                    loadCards(current, false)
                }
            }
        })
    }

    val editing = edit
    if (editing != null) {
        var createKey by remember(editing.id) { mutableStateOf(UUID.randomUUID().toString()) }
        CardEditor(editing, busy, message, onDismiss = { edit = null; message = "" }, onSave = { data ->
            if (demo) {
                val id = data.id.ifBlank { UUID.randomUUID().toString() }
                saveDemo(cards.filterNot { it.id == id } + data.copy(id = id))
                edit = null
            } else bound?.let { current ->
                runTask {
                    if (editing.id.isBlank()) {
                        api.write(current, "/v1/shortcuts", "POST", data.fields(), mapOf("Idempotency-Key" to createKey))
                    } else {
                        val payload = JSONObject()
                        val previous = editing.fields()
                        val changed = data.fields()
                        for (field in changed.keys()) {
                            if (previous.get(field).toString() != changed.get(field).toString()) {
                                payload.put(field, changed.get(field))
                            }
                        }
                        if (payload.length() > 0) api.write(current, "/v1/shortcuts/${editing.id}", "PATCH", payload,
                            mapOf("If-Match" to requireNotNull(etag)))
                    }
                    edit = null
                    loadCards(current, false)
                }
            }
        }, onChangeAfterError = {
            if (message.isNotBlank()) {
                message = ""
                createKey = UUID.randomUUID().toString()
            }
        }, onDelete = { edit = null; delete = editing }, onClear = {
            bound?.let { current -> runTask {
                api.write(current, "/v1/shortcuts/${editing.id}", "PATCH",
                    JSONObject().put("clear_override", true), mapOf("If-Match" to requireNotNull(etag)))
                edit = null
                loadCards(current, false)
            } }
        })
    }

    delete?.let { item ->
        AlertDialog(onDismissRequest = { if (!busy) delete = null }, title = { Text("移除卡片") },
            text = { Text("移除「${item.name}」？") }, confirmButton = {
                TextButton(enabled = !busy, onClick = {
                    if (demo) { saveDemo(cards.filterNot { it.id == item.id }); delete = null }
                    else bound?.let { current -> runTask {
                        api.write(current, "/v1/shortcuts/${item.id}", "DELETE",
                            headers = mapOf("If-Match" to requireNotNull(etag)))
                        delete = null
                        loadCards(current, false)
                    } }
                }) { Text("移除") }
            }, dismissButton = { TextButton(enabled = !busy, onClick = { delete = null }) { Text("取消") } })
    }

    if (unbind) {
        AlertDialog(onDismissRequest = { if (!busy) unbind = false }, title = { Text("解绑这台设备") },
            text = { Text("后端将撤销此设备的凭证，手机会清除配对信息和卡片缓存。") },
            confirmButton = { TextButton(enabled = !busy, onClick = {
                bound?.let { current -> runTask {
                    try { api.write(current, "/v1/devices/current", "DELETE") }
                    catch (e: ApiError) { if (e.status != 401) throw e }
                    unbind = false
                    reset()
                } }
            }) { Text("解绑") } }, dismissButton = {
                TextButton(enabled = !busy, onClick = { unbind = false }) { Text("取消") }
            })
    }
}

@Composable
private fun PairScreen(address: String, code: String, busy: Boolean, message: String,
    onAddress: (String) -> Unit, onCode: (String) -> Unit, onPair: () -> Unit, onDemo: () -> Unit) {
    LazyColumn(
        modifier = Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.safeDrawing).imePadding(),
        contentPadding = PaddingValues(horizontal = 24.dp, vertical = 32.dp),
        verticalArrangement = Arrangement.spacedBy(18.dp),
    ) {
        item {
            Card(colors = CardDefaults.cardColors(containerColor = spruce, contentColor = Color.White),
                shape = RoundedCornerShape(26.dp)) {
                Column(Modifier.fillMaxWidth().padding(26.dp), verticalArrangement = Arrangement.spacedBy(9.dp)) {
                    Text("▦", style = MaterialTheme.typography.headlineLarge, color = Color(0xFFBAF0D9))
                    Text("局域网服务", style = MaterialTheme.typography.headlineLarge, fontWeight = FontWeight.Bold)
                    Text("从手机打开自托管服务，查看 Ubuntu 状态和事件。", style = MaterialTheme.typography.bodyLarge)
                }
            }
        }
        item { Text("连接你的服务端", style = MaterialTheme.typography.titleLarge, fontWeight = FontWeight.SemiBold) }
        item {
            OutlinedTextField(address, onAddress, Modifier.fillMaxWidth(), singleLine = true, enabled = !busy,
                label = { Text("HTTPS 地址") }, placeholder = { Text("https://hub.example.net") })
        }
        item {
            OutlinedTextField(code, onCode, Modifier.fillMaxWidth(), singleLine = true, enabled = !busy,
                visualTransformation = PasswordVisualTransformation(), label = { Text("一次性配对码") })
        }
        if (message.isNotBlank()) item { StatusLine(message, true) }
        item {
            Button(onPair, Modifier.fillMaxWidth().heightIn(min = 50.dp),
                enabled = !busy && address.isNotBlank() && code.isNotBlank()) {
                Text(if (busy) "正在验证并配对…" else "连接并配对")
            }
        }
        item {
            OutlinedButton(onDemo, Modifier.fillMaxWidth().heightIn(min = 50.dp), enabled = !busy) {
                Text("体验离线演示")
            }
        }
    }
}

@Composable
internal fun StatusLine(text: String, error: Boolean = false, modifier: Modifier = Modifier) {
    Surface(modifier.fillMaxWidth(), shape = RoundedCornerShape(14.dp),
        color = if (error) MaterialTheme.colorScheme.errorContainer else MaterialTheme.colorScheme.secondaryContainer) {
        Text(text, Modifier.padding(horizontal = 14.dp, vertical = 11.dp),
            color = if (error) MaterialTheme.colorScheme.onErrorContainer else MaterialTheme.colorScheme.onSecondaryContainer,
            style = MaterialTheme.typography.bodyMedium)
    }
}

@Composable
private fun Services(cards: List<ServiceCard>, demo: Boolean, online: Boolean, busy: Boolean, importNote: String,
    onOpen: (ServiceCard) -> Unit, onEdit: (ServiceCard) -> Unit, onOrder: (String) -> Unit) {
    LazyColumn(modifier = Modifier.fillMaxSize(), contentPadding = PaddingValues(20.dp, 10.dp, 20.dp, 92.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp)) {
        if (demo) item { StatusLine("演示卡片保存在这台手机。测试项目可在应用内直接打开。") }
        if (!demo && !online) item { StatusLine("后端尚未连通。已缓存的卡片仍可打开，编辑需恢复连接。") }
        if (importNote.isNotBlank()) item { StatusLine(importNote) }
        if (cards.isEmpty()) item {
            Text("暂无服务卡片", style = MaterialTheme.typography.titleLarge)
            Text(if (online || demo) "新增 HTTPS 服务，或由管理员导入导航。" else "恢复连接后可刷新卡片。")
        }
        for ((group, entries) in cards.groupBy { it.group }) {
            item {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text(group, Modifier.weight(1f).padding(top = 10.dp, bottom = 2.dp),
                        style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold)
                    if (entries.size > 1) {
                        TextButton(enabled = (demo || online) && !busy, onClick = { onOrder(group) }) { Text("排序") }
                    }
                }
            }
            items(entries, key = { it.id }) { item ->
                Card(colors = CardDefaults.cardColors(containerColor = Color.White),
                    shape = RoundedCornerShape(18.dp)) {
                    Row(Modifier.fillMaxWidth().padding(end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                        Surface(onClick = { onOpen(item) }, modifier = Modifier.weight(1f),
                            color = Color.White, shape = RoundedCornerShape(18.dp)) {
                            Row(Modifier.padding(14.dp), verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                                Surface(shape = RoundedCornerShape(12.dp), color = mint) {
                                    Box(Modifier.size(42.dp), contentAlignment = Alignment.Center) {
                                        Text(item.icon.ifBlank { "↗" }.take(2), color = spruce,
                                            style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold)
                                    }
                                }
                                Column(Modifier.weight(1f)) {
                                    Text(item.name, style = MaterialTheme.typography.titleMedium,
                                        maxLines = 1, overflow = TextOverflow.Ellipsis)
                                    Text(if (item.lan == localDemo) "内嵌网页" else item.lan.removePrefix("https://"),
                                        style = MaterialTheme.typography.bodySmall,
                                        color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1,
                                        overflow = TextOverflow.Ellipsis)
                                }
                            }
                        }
                        TextButton(enabled = demo || online, onClick = { onEdit(item) }) { Text("编辑") }
                    }
                }
            }
        }
    }
}
