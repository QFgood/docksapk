package io.github.qfgood.docksapk

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.net.http.SslError
import android.webkit.CookieManager
import android.webkit.SslErrorHandler
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.webkit.WebViewAssetLoader
import java.io.ByteArrayInputStream

@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun ProjectPage(card: ServiceCard, preferRemote: Boolean, demoAsset: String, onClose: () -> Unit) {
    val context = LocalContext.current
    var address by remember(card.id, preferRemote) {
        mutableStateOf(if (preferRemote) card.remote.ifBlank { card.lan } else card.lan)
    }
    val demo = card.lan == demoAsset && card.id == "demo-project"
    val trusted = remember(card) {
        (listOf(card.lan, card.remote) + card.origins).mapNotNull {
            runCatching { webOrigin(it) }.getOrNull()
        }.toSet()
    }
    fun allowed(value: String): Boolean = runCatching {
        val origin = webOrigin(value)
        origin in trusted && (origin != "https://appassets.androidplatform.net" || demo)
    }.getOrDefault(false)

    val assets = remember(context, demo) {
        if (demo) WebViewAssetLoader.Builder().addPathHandler("/assets/", WebViewAssetLoader.AssetsPathHandler(context)).build()
        else null
    }
    var web by remember { mutableStateOf<WebView?>(null) }
    var error by remember { mutableStateOf("") }
    var progress by remember { mutableIntStateOf(0) }
    var more by remember { mutableStateOf(false) }
    var file by remember { mutableStateOf<ValueCallback<Array<Uri>>?>(null) }
    val chooser = rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        file?.onReceiveValue(WebChromeClient.FileChooserParams.parseResult(result.resultCode, result.data))
        file = null
    }

    fun goBack() {
        val view = web
        if (view?.canGoBack() == true) { error = ""; view.goBack() } else onClose()
    }

    fun external(value: String) {
        if (!allowed(value) || demo) return
        val result = runCatching { context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(value))) }
        if (result.isFailure) error = "没有可打开此项目的浏览器。"
    }

    fun activeUrl(): String = web?.url?.takeIf(::allowed) ?: address

    BackHandler { goBack() }
    DisposableEffect(Unit) {
        onDispose {
            file?.onReceiveValue(null)
            CookieManager.getInstance().flush()
        }
    }

    Scaffold(topBar = {
        TopAppBar(title = { Text(card.name, maxLines = 1, overflow = TextOverflow.Ellipsis) },
            navigationIcon = { TextButton(onClick = ::goBack) { Text("返回") } },
            actions = {
                TextButton(onClick = { error = ""; web?.reload() }) { Text("刷新") }
                Box {
                    TextButton(onClick = { more = true }) { Text("更多") }
                    DropdownMenu(more, { more = false }) {
                        DropdownMenuItem(text = { Text("回服务列表") }, onClick = { more = false; onClose() })
                        if (card.remote.isNotBlank() && card.remote != card.lan) {
                            DropdownMenuItem(text = { Text(if (address == card.lan) "打开远程地址" else "打开局域网地址") },
                                onClick = {
                                    more = false
                                    address = if (address == card.lan) card.remote else card.lan
                                    error = ""
                                    if (allowed(address)) web?.loadUrl(address)
                                })
                        }
                        if (!demo) {
                            DropdownMenuItem(text = { Text("复制项目地址") }, onClick = {
                                more = false
                                context.getSystemService(ClipboardManager::class.java)
                                    .setPrimaryClip(ClipData.newPlainText(card.name, activeUrl()))
                            })
                            DropdownMenuItem(text = { Text("用系统浏览器打开") }, onClick = {
                                more = false
                                external(activeUrl())
                            })
                        }
                    }
                }
            })
    }) { padding ->
        Column(Modifier.fillMaxSize().padding(padding).imePadding()) {
            if (progress in 1..99) LinearProgressIndicator(progress = { progress / 100f }, modifier = Modifier.fillMaxWidth())
            if (error.isNotBlank()) StatusLine(error, true, Modifier.padding(horizontal = 12.dp, vertical = 8.dp))
            AndroidView(
                modifier = Modifier.fillMaxWidth().weight(1f),
                factory = { ctx ->
                    WebView(ctx).apply {
                        settings.javaScriptEnabled = true
                        settings.domStorageEnabled = true
                        settings.allowFileAccess = false
                        settings.allowContentAccess = false
                        settings.javaScriptCanOpenWindowsAutomatically = false
                        settings.setSupportMultipleWindows(false)
                        settings.mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
                        settings.safeBrowsingEnabled = true
                        settings.mediaPlaybackRequiresUserGesture = true
                        CookieManager.getInstance().setAcceptThirdPartyCookies(this, false)
                        webViewClient = object : WebViewClient() {
                            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                                val value = request.url.toString()
                                if (request.isForMainFrame && !allowed(value)) {
                                    error = "已拦截未登记的项目跳转。可在卡片中登记可信 HTTPS 来源。"
                                    return true
                                }
                                return request.url.scheme != "https"
                            }

                            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
                                val allowedRequest = request.url.scheme == "https" &&
                                    (!request.isForMainFrame || allowed(request.url.toString()))
                                if (!allowedRequest) {
                                    return WebResourceResponse("text/plain", "UTF-8", 403, "Blocked", emptyMap(),
                                        ByteArrayInputStream(ByteArray(0)))
                                }
                                return assets?.shouldInterceptRequest(request.url)
                            }

                            override fun onPageStarted(view: WebView, url: String, favicon: android.graphics.Bitmap?) {
                                if (!allowed(url)) {
                                    view.stopLoading()
                                    error = "已拦截未登记的项目跳转。"
                                }
                            }

                            override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, failure: SslError) {
                                handler.cancel()
                                error = "项目 HTTPS 证书验证失败，加载已终止。"
                            }

                            override fun onReceivedError(view: WebView, request: WebResourceRequest, failure: WebResourceError) {
                                if (request.isForMainFrame) {
                                    progress = 100
                                    if (error.isBlank()) error = "项目暂不可达，请检查 HTTPS 地址及网络。"
                                }
                            }

                            override fun onPageFinished(view: WebView, url: String) {
                                CookieManager.getInstance().flush()
                            }
                        }
                        webChromeClient = object : WebChromeClient() {
                            override fun onProgressChanged(view: WebView, newProgress: Int) { progress = newProgress }

                            override fun onShowFileChooser(view: WebView, callback: ValueCallback<Array<Uri>>,
                                params: FileChooserParams): Boolean {
                                if (!allowed(view.url.orEmpty()) || demo) return false
                                file?.onReceiveValue(null)
                                file = callback
                                val launched = runCatching { chooser.launch(params.createIntent()) }
                                if (launched.isFailure) {
                                    file = null
                                    callback.onReceiveValue(null)
                                    error = "无法打开系统文件选择器。"
                                }
                                return true
                            }
                        }
                        setDownloadListener { _, _, _, _, _ ->
                            error = "该项目请求下载文件。请在「更多」中打开系统浏览器后下载。"
                        }
                        web = this
                        if (allowed(address)) loadUrl(address) else error = "项目地址不符合 HTTPS 要求。"
                    }
                },
                onRelease = { view -> view.stopLoading(); view.destroy() },
            )
        }
    }
}
