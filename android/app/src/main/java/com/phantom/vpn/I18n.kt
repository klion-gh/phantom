package com.phantom.vpn

import android.content.Context
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue

enum class Lang { RU, EN }

/**
 * App UI string translations. The app was originally Russian-only; English was
 * added via a language toggle in Settings.
 *
 * [lang] is a Compose state, so any composable that calls [t] recomposes when
 * the language changes. The foreground service (not Compose) reads [t] too - it
 * runs in the same process and [lang] is loaded early in
 * PhantomApplication.onCreate, so it always has the current value. Language is
 * persisted in plain SharedPreferences (it isn't sensitive).
 */
object I18n {
    private const val PREFS = "phantom_settings"
    private const val KEY = "language"

    var lang by mutableStateOf(Lang.RU)
        private set

    fun load(context: Context) {
        val saved = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString(KEY, null)
        lang = if (saved == "en") Lang.EN else Lang.RU
    }

    fun set(context: Context, newLang: Lang) {
        lang = newLang
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .edit()
            .putString(KEY, if (newLang == Lang.EN) "en" else "ru")
            .apply()
    }

    fun t(key: String): String =
        strings[lang]?.get(key) ?: strings[Lang.RU]?.get(key) ?: key

    fun t(key: String, vararg args: Any?): String = String.format(t(key), *args)

    private val strings: Map<Lang, Map<String, String>> = mapOf(
        Lang.RU to mapOf(
            // main / config list
            "configs" to "Конфигурации",
            "version" to "Версия",
            "insecure_storage" to "Хранилище ключей недоступно — конфигурации сохранены без шифрования. Тот, кто получит доступ к устройству, прочитает PSK и адрес сервера.",
            "no_configs" to "Нет добавленной конфигурации",
            "no_configs_hint" to "Нажмите + чтобы добавить client.yaml",
            "resources" to "Доступность ресурсов",
            "no_resources" to "Нет добавленных ресурсов",
            "no_resources_hint" to "Нажмите + чтобы добавить сайт для проверки",
            // add/edit config
            "add_config_title" to "Добавить конфигурацию",
            "edit_config_title" to "Редактировать конфигурацию",
            "paste_yaml" to "Вставьте содержимое client.yaml целиком:",
            "save" to "Сохранить",
            "delete_config" to "Удалить конфигурацию",
            "delete_config_q" to "Удалить конфигурацию?",
            "delete_config_text" to "Придётся снова вставить client.yaml, чтобы подключиться этим профилем.",
            "delete" to "Удалить",
            "cancel" to "Отмена",
            // add resource dialog
            "add_resource" to "Добавить ресурс",
            "resource_name_ph" to "Название, например Netflix",
            "add" to "Добавить",
            // settings / log
            "settings" to "Настройки",
            "view_log" to "Посмотреть лог",
            "log_title" to "Лог (%s)",
            "share" to "Поделиться",
            "show_proxy_settings" to "Отобразить настройки прокси",
            "language" to "Язык",
            "palette" to "Палитра",
            "background" to "Фон",
            "palette_midnight" to "Полночь",
            "palette_emerald" to "Изумруд",
            "palette_sunset" to "Закат",
            "palette_ocean" to "Океан",
            "palette_graphite" to "Графит",
            "palette_sakura" to "Сакура",
            "background_orbs_label" to "Сферы",
            "background_orbs_desc" to "Плавно плывущие пятна света",
            "background_aurora_label" to "Сияние",
            "background_aurora_desc" to "Медленные цветные ленты",
            "background_stars_label" to "Звёзды",
            "background_stars_desc" to "Мерцающие точки на фоне",
            "background_mesh_label" to "Сеть",
            "background_mesh_desc" to "Точки, соединённые тонкими линиями",
            "background_meteors_label" to "Метеоры",
            "background_meteors_desc" to "Редкие росчерки по диагонали",
            "background_waves_label" to "Волны",
            "background_waves_desc" to "Слоистые волнистые линии",
            "background_embers_label" to "Искры",
            "background_embers_desc" to "Огоньки, поднимающиеся снизу вверх",
            "background_plain_label" to "Без анимации",
            "background_plain_desc" to "Только фоновый градиент",
            // tiles
            "ping" to "Пинг",
            "ms" to "мс",
            "port_ph" to "порт",
            "checking" to "Проверка...",
            "available" to "Доступен",
            "unavailable" to "Недоступен",
            // toasts / proxy
            "download_failed" to "Не удалось скачать обновление",
            "bad_port" to "Некорректный порт: %s",
            "proxy_failed" to "Не удалось включить прокси на порту %1\$s: %2\$s",
            "proxy_any" to "(любой)",
            // notification
            "active" to "активен",
            "inactive" to "неактивен",
            "connecting" to "подключение...",
            "error_short" to "ошибка",
            "disconnect_vpn" to "Отключить VPN",
            "cancel_action" to "Отменить",
            "connect_vpn" to "Подключить VPN",
            "disconnect_proxy" to "Отключить Proxy",
            "connect_proxy" to "Подключить Proxy",
        ),
        Lang.EN to mapOf(
            "configs" to "Configurations",
            "version" to "Version",
            "insecure_storage" to "Keystore unavailable — configurations are stored unencrypted. Anyone with access to this device can read the PSK and server address.",
            "no_configs" to "No configuration added",
            "no_configs_hint" to "Tap + to add client.yaml",
            "resources" to "Resource availability",
            "no_resources" to "No resources added",
            "no_resources_hint" to "Tap + to add a site to check",
            "add_config_title" to "Add configuration",
            "edit_config_title" to "Edit configuration",
            "paste_yaml" to "Paste the full client.yaml contents:",
            "save" to "Save",
            "delete_config" to "Delete configuration",
            "delete_config_q" to "Delete configuration?",
            "delete_config_text" to "You will have to paste client.yaml again to connect with this profile.",
            "delete" to "Delete",
            "cancel" to "Cancel",
            "add_resource" to "Add resource",
            "resource_name_ph" to "Name, e.g. Netflix",
            "add" to "Add",
            "settings" to "Settings",
            "view_log" to "View log",
            "log_title" to "Log (%s)",
            "share" to "Share",
            "show_proxy_settings" to "Show proxy settings",
            "language" to "Language",
            "palette" to "Palette",
            "background" to "Background",
            "palette_midnight" to "Midnight",
            "palette_emerald" to "Emerald",
            "palette_sunset" to "Sunset",
            "palette_ocean" to "Ocean",
            "palette_graphite" to "Graphite",
            "palette_sakura" to "Sakura",
            "background_orbs_label" to "Orbs",
            "background_orbs_desc" to "Softly drifting patches of light",
            "background_aurora_label" to "Aurora",
            "background_aurora_desc" to "Slow-moving coloured ribbons",
            "background_stars_label" to "Stars",
            "background_stars_desc" to "Twinkling points across the backdrop",
            "background_mesh_label" to "Mesh",
            "background_mesh_desc" to "Dots connected by thin lines",
            "background_meteors_label" to "Meteors",
            "background_meteors_desc" to "Occasional diagonal streaks",
            "background_waves_label" to "Waves",
            "background_waves_desc" to "Layered wavy lines",
            "background_embers_label" to "Embers",
            "background_embers_desc" to "Sparks drifting upward",
            "background_plain_label" to "No animation",
            "background_plain_desc" to "Background gradient only",
            "ping" to "Ping",
            "ms" to "ms",
            "port_ph" to "port",
            "checking" to "Checking...",
            "available" to "Available",
            "unavailable" to "Unavailable",
            "download_failed" to "Failed to download the update",
            "bad_port" to "Invalid port: %s",
            "proxy_failed" to "Couldn’t start the proxy on port %1\$s: %2\$s",
            "proxy_any" to "(any)",
            "active" to "active",
            "inactive" to "inactive",
            "connecting" to "connecting...",
            "error_short" to "error",
            "disconnect_vpn" to "Disconnect VPN",
            "cancel_action" to "Cancel",
            "connect_vpn" to "Connect VPN",
            "disconnect_proxy" to "Disconnect Proxy",
            "connect_proxy" to "Connect Proxy",
        ),
    )
}
