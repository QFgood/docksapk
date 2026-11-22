package io.github.qfgood.docksapk

import android.annotation.SuppressLint
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent

internal data class NoticeRoute(val instance: String, val event: String) {
    companion object {
        private const val action = "io.github.qfgood.docksapk.OPEN_EVENT"
        private val id = Regex("[0-9a-f]{32}")

        val demo = NoticeRoute("demo", "demo-event")

        fun from(intent: Intent): NoticeRoute? {
            if (intent.action != action) return null
            val instance = intent.getStringExtra("instance") ?: return null
            val event = intent.getStringExtra("event") ?: return null
            val route = NoticeRoute(instance, event)
            return route.takeIf { it == demo || (id.matches(instance) && id.matches(event)) }
        }
    }

    fun intent(context: Context) = Intent(context, MainActivity::class.java)
        .setAction(action)
        .addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        .putExtra("instance", instance)
        .putExtra("event", event)
}

internal object LocalNotice {
    private const val channelId = "local-event-test"

    fun clear(context: Context) = context.getSystemService(NotificationManager::class.java).cancelAll()

    @SuppressLint("MissingPermission")
    fun show(context: Context, route: NoticeRoute): Boolean {
        val manager = context.getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(NotificationChannel(channelId, "本机测试", NotificationManager.IMPORTANCE_DEFAULT))
        if (!manager.areNotificationsEnabled() || manager.getNotificationChannel(channelId).importance == NotificationManager.IMPORTANCE_NONE) {
            return false
        }
        val notificationId = route.hashCode()
        val tap = PendingIntent.getActivity(context, notificationId, route.intent(context),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val notification = Notification.Builder(context, channelId)
            .setSmallIcon(R.drawable.ic_notice)
            .setContentTitle("局域网服务 · 本机测试")
            .setContentText("点此打开事件详情")
            .setContentIntent(tap)
            .setAutoCancel(true)
            .setVisibility(Notification.VISIBILITY_PRIVATE)
            .build()
        manager.notify(notificationId, notification)
        return true
    }
}
