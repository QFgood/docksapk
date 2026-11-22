package io.github.qfgood.docksapk

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.net.ConnectException
import java.net.IDN
import java.net.SocketTimeoutException
import java.net.URI
import java.net.UnknownHostException
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.SSLException

data class Session(
    val id: String,
    val name: String,
    val address: String,
    val token: String,
    val device: String,
    val lan: String = "",
    val remote: String = "",
) {
    fun json() = JSONObject().put("id", id).put("name", name).put("address", address)
        .put("token", token).put("device", device).put("lan", lan).put("remote", remote)

    companion object {
        fun from(j: JSONObject) = Session(j.getString("id"), j.getString("name"), j.getString("address"),
            j.getString("token"), j.getString("device"), j.optString("lan"), j.optString("remote"))
    }
}

data class ServiceCard(
    val id: String = "",
    val name: String = "",
    val group: String = "常用服务",
    val position: Int = 0,
    val icon: String = "",
    val lan: String = "",
    val remote: String = "",
    val origins: List<String> = emptyList(),
    val source: String = "manual",
    val overridden: Boolean = false,
) {
    fun fields() = JSONObject().put("name", name).put("group_id", group).put("position", position)
        .put("icon", icon).put("lan_url", lan).put("tailnet_url", remote)
        .put("trusted_origins", JSONArray(origins))

    fun json() = fields().put("id", id).put("source", source).put("overridden", overridden)

    companion object {
        fun from(j: JSONObject) = ServiceCard(
            id = j.getString("id"), name = j.getString("name"), group = j.getString("group_id"),
            position = j.getInt("position"), icon = j.optString("icon"), lan = j.getString("lan_url"),
            remote = j.optString("tailnet_url"), origins = j.optJSONArray("trusted_origins").strings(),
            source = j.optString("source", "manual"), overridden = j.optBoolean("overridden"),
        )
    }
}

fun JSONArray?.objects(): List<JSONObject> =
    if (this == null) emptyList() else (0 until length()).map { getJSONObject(it) }

fun JSONArray?.strings(): List<String> =
    if (this == null) emptyList() else (0 until length()).map { getString(it) }

class LocalStore(context: Context) {
    private val prefs = context.getSharedPreferences("docks-hub", Context.MODE_PRIVATE)
    private val keys = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
    private val alias = "docks-session-v1"

    private fun key(): SecretKey {
        val existing = keys.getKey(alias, null)
        if (existing is SecretKey) return existing
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").apply {
            init(KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .build())
        }.generateKey()
    }

    fun session(): Session? {
        val encoded = prefs.getString("session", null) ?: return null
        return try {
            val parts = encoded.split('.')
            val iv = Base64.decode(parts[0], Base64.NO_WRAP)
            val payload = Base64.decode(parts[1], Base64.NO_WRAP)
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, iv))
            Session.from(JSONObject(cipher.doFinal(payload).decodeToString()))
        } catch (_: Exception) {
            clear()
            null
        }
    }

    fun save(session: Session) {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val iv = Base64.encodeToString(cipher.iv, Base64.NO_WRAP)
        val content = Base64.encodeToString(cipher.doFinal(session.json().toString().encodeToByteArray()), Base64.NO_WRAP)
        check(prefs.edit().putString("session", "$iv.$content").commit()) { "无法保存设备会话" }
    }

    fun cache(id: String, list: JSONObject) {
        prefs.edit().putString("cards-$id", list.toString()).apply()
    }

    fun cache(id: String): JSONObject? =
        prefs.getString("cards-$id", null)?.let { runCatching { JSONObject(it) }.getOrNull() }

    fun saveHost(id: String, sample: JSONObject) {
        prefs.edit().putString("host-$id", sample.toString()).apply()
    }

    fun cachedHost(id: String): JSONObject? =
        prefs.getString("host-$id", null)?.let { runCatching { JSONObject(it) }.getOrNull() }

    fun clear() {
        prefs.edit().clear().commit()
        if (keys.containsAlias(alias)) keys.deleteEntry(alias)
    }
}

class ApiError(val status: Int, val code: String) : IOException(code)
class InstanceMismatch : IOException()
class UnsupportedApi : IOException()
class BadAddress : IllegalArgumentException()

fun webOrigin(value: String): String {
    val uri = runCatching { URI(value) }.getOrElse { throw BadAddress() }
    val host = uri.host ?: throw BadAddress()
    if (!uri.scheme.equals("https", true) || uri.rawUserInfo != null || uri.port > 65535 ||
        uri.port == 0 || value.contains('\\')) throw BadAddress()
    val normalized = if (host.startsWith("[")) host.lowercase() else IDN.toASCII(host).lowercase()
    return "https://$normalized" + if (uri.port in 1..65535 && uri.port != 443) ":${uri.port}" else ""
}

fun apiOrigin(value: String): String {
    val input = value.trim()
    val origin = webOrigin(input)
    val uri = URI(input)
    if ((uri.rawPath != "" && uri.rawPath != "/") || uri.rawQuery != null || uri.rawFragment != null) {
        throw BadAddress()
    }
    return origin
}

data class Reply(val data: JSONObject, val etag: String?, val status: Int)

class HubApi(initial: String) {
    var active = initial
        private set

    private fun request(
        address: String,
        path: String,
        method: String = "GET",
        token: String? = null,
        payload: JSONObject? = null,
        headers: Map<String, String> = emptyMap(),
    ): Reply {
        val c = URI(address + path).toURL().openConnection() as HttpsURLConnection
        return try {
            c.instanceFollowRedirects = false
            c.connectTimeout = 3800
            c.readTimeout = 6500
            c.requestMethod = method
            c.setRequestProperty("Accept", "application/json")
            if (token != null) c.setRequestProperty("Authorization", "Bearer $token")
            headers.forEach { (name, value) -> c.setRequestProperty(name, value) }
            if (payload != null) {
                val bytes = payload.toString().encodeToByteArray()
                c.doOutput = true
                c.setFixedLengthStreamingMode(bytes.size)
                c.setRequestProperty("Content-Type", "application/json")
                c.outputStream.use { it.write(bytes) }
            }
            val status = c.responseCode
            if (status == 304 || status == 204) return Reply(JSONObject(), c.getHeaderField("ETag"), status)
            if (status in 300..399) throw ApiError(status, "redirect")
            val stream = if (status in 200..299) c.inputStream else c.errorStream
            val text = stream?.bufferedReader()?.use { reader ->
                val chars = CharArray(4096)
                val output = StringBuilder()
                while (true) {
                    val n = reader.read(chars)
                    if (n < 0) break
                    if (output.length + n > 4_000_000) throw IOException("response_too_large")
                    output.append(chars, 0, n)
                }
                output.toString()
            } ?: ""
            val body = runCatching { JSONObject(text) }.getOrNull()
            if (status !in 200..299) throw ApiError(status, body?.optString("error").orEmpty())
            Reply(body ?: throw IOException("invalid_response"), c.getHeaderField("ETag"), status)
        } finally {
            c.disconnect()
        }
    }

    private fun about(address: String): JSONObject {
        val info = request(address, "/v1/about").data
        if (info.optInt("api_version") != 1 || info.optString("instance_id").isBlank()) throw UnsupportedApi()
        return info
    }

    suspend fun pair(code: String): Session = withContext(Dispatchers.IO) {
        val info = about(active)
        val id = info.getString("instance_id")
        val name = "${Build.MANUFACTURER} ${Build.MODEL}".trim().take(80)
        val request = JSONObject().put("code", code.trim()).put("device_name", name).put("instance_id", id)
        val paired = request(active, "/v1/pair", "POST", payload = request).data
        if (paired.getString("instance_id") != id) throw InstanceMismatch()
        Session(id, info.getString("name"), active, paired.getString("token"), paired.getJSONObject("device").getString("name"))
    }

    suspend fun read(session: Session, path: String, headers: Map<String, String> = emptyMap()): Reply =
        withContext(Dispatchers.IO) {
            var error: Exception? = null
            val candidates = listOf(active, session.address, session.lan, session.remote).filter { it.isNotBlank() }.distinct()
            for (address in candidates) {
                try {
                    verify(session, address)
                    return@withContext request(address, path, token = session.token, headers = headers).also { active = address }
                } catch (e: ApiError) {
                    if (e.status in 400..499) throw e
                    if (error == null) error = e
                } catch (e: IOException) {
                    if (error == null) error = e
                }
            }
            throw error ?: BadAddress()
        }

    suspend fun write(session: Session, path: String, method: String, payload: JSONObject? = null,
        headers: Map<String, String> = emptyMap()): Reply = withContext(Dispatchers.IO) {
        verify(session, active)
        request(active, path, method, session.token, payload, headers)
    }

    private fun verify(session: Session, address: String) {
        if (about(address).getString("instance_id") != session.id) throw InstanceMismatch()
    }

    suspend fun diagnose(session: Session, address: String) = withContext(Dispatchers.IO) {
        verify(session, apiOrigin(address))
    }
}

fun explain(error: Throwable): String = when (error) {
    is BadAddress -> "请输入有效的 HTTPS 地址。"
    is InstanceMismatch -> "此地址返回了不同的实例，设备凭证未发送。"
    is UnsupportedApi -> "服务端 API 版本不兼容，或此地址没有运行局域网服务后端。"
    is UnknownHostException -> "DNS 无法解析服务端地址。"
    is SocketTimeoutException -> "连接超时，请检查 Wi-Fi 或 Tailscale。"
    is ConnectException -> "服务未响应，请检查地址、端口及网络。"
    is SSLException -> "HTTPS 证书或 TLS 验证失败，请检查证书、域名及系统时间。"
    is ApiError -> when {
        error.code == "invalid_or_expired_code" -> "配对码错误、已使用或已过期。"
        error.code == "invalid_pair_request" -> "配对码应为 12 位字符，请检查后重试。"
        error.code == "invalid_shortcut" -> "卡片内容无效，请检查名称、排序和 HTTPS 地址。"
        error.code == "invalid_device" -> "这台设备的凭证已失效，请重新配对。"
        error.code == "redirect" -> "后端返回了跳转，请填写最终 HTTPS 地址。"
        error.status == 412 -> "另一台设备已修改数据，请使用刷新后的版本重试。"
        error.status == 429 -> "请求过于频繁，请稍后重试。"
        error.status == 404 -> "记录不存在或已超过保留期限。"
        else -> "服务端请求失败（HTTP ${error.status}）。"
    }
    else -> "暂时无法完成请求，请检查网络并重试。"
}
