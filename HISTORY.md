# История Phantom

Журнал версий и действий. **Новые записи — сверху.** Пополняется после каждой завершённой работы (правило в CLAUDE.md).

## Действия

- 2026-11-25 — **v2.8.5-beta.1**, IPv6 как обход блокировки IPv4 (все клиенты): сервер резолвится сразу в A и AAAA (`transport.LookupDual`, ответ AAAA ждём не дольше 400 мс после A), адреса перебираются гонкой IPv4/IPv6 со сдвигом 300 мс (`dialRace`, как RFC 8305) — сервер с заблокированным IPv4 и живым IPv6 подключается за доли секунды вместо 10-секундного таймаута. Пинги на плитках тоже двухстековые и показывают тот адрес, что ответил. Windows: в туннель идут оба семейства (IPv6 без обходных маршрутов — туннель забирает только IPv4). Не проверено вживую: у домашнего провайдера нет IPv6. Следующий шаг: пользователь добавляет AAAA-записи и проверяет с мобильного интернета.
- 2026-11-25 — у одного заблокированного по IPv4 сервера проверен IPv6: на сервере настроен и работает, Phantom по IPv6 отвечает снаружи; у домена нет AAAA-записи. Клиенты: туннель на Android/Linux пробует IPv6 только после 10-секундного таймаута IPv4, пинги и туннель на Windows — только IPv4. Следующий шаг: AAAA-запись; при желании — Happy Eyeballs и IPv6 в пингах и на Windows.
- 2026-11-25 — проверены способы обойти блокировку IP трёх серверов без смены адреса. Российский ретранслятор не помогает: из его дата-центра те же адреса тоже недоступны (два целиком, у третьего замораживается TLS), то есть ТСПУ режет и трафик дата-центров. У одного сервера есть IPv6, но проверить его отсюда нельзя (у домашнего провайдера IPv6 нет). Вывод: менять IP.
- 2026-11-25 — диагностика «Пинг: нет ответа» у трёх серверов: Phantom на них работает, причина — блокировка из РФ (у двух недоступен весь IP, у одного TCP проходит, но TLS замораживается при любом SNI). Подробности — в локальном SERVERS.md. Следующий шаг: новые IP для этих серверов и обновление DNS.
- 2026-11-25 — дашборд (`cmd/console`): раздел «История» с вкладками «Действия» и «Версии», читает `HISTORY.md` из репозитория при каждой загрузке (`GET /api/history`, флаг `-history`). Раздел «Версии» в `HISTORY.md` пересобран из git-тегов (все релизы до v2.8.4, бета помечены). Следующий шаг: держать «Версии» в актуальном состоянии при каждом релизе.
- 2026-11-19 — **v2.8.4 стабильная**: тот же код, что v2.8.4-beta.1. Список изменений относительно 2.7.1 — в описании релиза на GitHub (блокировка рекламы YouTube, «Приложения под обход», «Поддерживать соединение», Claude и разделы в каталоге, новый вид главного экрана, надёжнее Умный VPN и Discord). Следующий шаг: отзывы с реальных устройств, Linux — блокировка рекламы и приложения под обход через phantomd.
- 2026-11-19 — **v2.8.4-beta.1**: Android — у островка навигации градиентная рамка 1dp, как у подключённой конфигурации (BrandGradient, следует выбранной палитре); размытие под островком слабее (blurRadius 24 → 12dp). Windows — островок вдвое шире: 378px (~10 см), но не шире окна минус 32px. Проверено на эмуляторе и в превью браузера.
- 2026-11-19 — **v2.8.3-beta.1**, доработка дизайна по отзыву:
  - Android: островок навигации снова во всю ширину (как до 2.8.2), размытие осталось.
  - Шапка (логотип, обновление, настройки) больше не закреплена: она — верх страницы и уезжает вместе с прокруткой, линии между шапкой и списком нет. Android: `HeaderScroll` (NestedScrollConnection) + `CollapsingHeader` в `MainActivity.kt` — прокрутка вверх сначала уводит шапку, вниз — возвращает её, когда список дошёл до начала; после ухода шапки страница идёт под строку состояния и там растворяется. Заголовки «Маршрутизация» и «Доступность ресурсов» перенесены внутрь списков. Windows: блок `#main-top` переносится в прокрутку видимого раздела (`showSection`), заголовки разделов — внутрь прокрутки; маски растворения у списков главного экрана убраны.
  - Проверено: Android на эмуляторе (API 34), Windows в превью браузера. На реальных устройствах не смотрел.
- 2026-11-19 — **v2.8.2-beta.1**, дизайн без рамок сверху и снизу:
  - Нижняя навигация теперь плавающий островок без рамки: полупрозрачный материал с размытием того, что под ним; списки прокручиваются под ним до самого края окна/экрана. Windows: по центру, фиксированная ширина 189px (~5 см при 96 dpi), `backdrop-filter` (`desktop/frontend/src/style.css`). Android: размытие через библиотеку Haze (`dev.chrisbanes.haze:haze:0.7.3`), источник — весь экран вместе с анимированным фоном, островок рисуется поверх (`NavIslandHost` в `MainActivity.kt`); настоящий блюр на Android 12+, ниже — непрозрачнее тинт.
  - Сверху вместо обрыва под шапкой списки плавно растворяются (маска 22px: CSS `mask-image`, на Android `Modifier.fadeTopEdge`). Шапка на месте.
  - Android: системная нижняя вставка больше не обрезает главный экран; остальные экраны (настройки, лог, каталог, выбор приложений) сами отступают от навигационной панели.
  - Проверено: Windows-интерфейс в превью браузера (заглушка бэкенда), Android — на эмуляторе (API 34). На реальном телефоне и на Android ниже 12 не смотрел.
- 2026-11-18 — **v2.8.1-beta.1**:
  - Android: в «Приложения под обход» видны все установленные приложения, а не пара десятков системных — в манифест добавлен `<queries>` (MAIN/LAUNCHER), без него Android 11+ скрывает чужие пакеты.
  - «Популярные ресурсы» (оба клиента, каталог общий в `internal/routing/catalog.go`): убран Slack, добавлен Claude (claude.ai, claude.com, anthropic.com, claudeusercontent.com, claude.site, clau.de). Каталог разбит на разделы с заголовками: Соцсети, Музыка, ИИ, затем Видео и Другое (поле `category`).
  - Номер 2.8.1, а не 2.8.0-beta.2: обновлятор сравнивает версии без суффикса, и beta.2 не предложился бы тем, кто уже на 2.8.0-beta.1.
  - Следующий шаг: проверить на телефоне список приложений и разделы каталога.
- 2026-11-18 — **v2.8.0-beta.1**, оба клиента:
  - «Блокировать рекламу YouTube» (переключатель на странице конфигураций): рекламные хосты Google/YouTube (doubleclick, googlesyndication, googleadservices, IMA SDK, 2mdn…) получают NXDOMAIN прямо на устройстве, в любом режиме, без переподключения (`internal/routing/adblock.go`, `RouteBlock` в netstack/split DNS). На Windows, пока включено, отклоняется DoH/DoT к 1.1.1.1/8.8.8.8, чтобы Chrome не обходил блокировку. Ограничение: видеореклама, вшитая в поток googlevideo.com, по имени не отличается и остаётся. На Linux пока нет (движок в phantomd).
  - «Подключиться к лучшей» → «Подключено» с анимацией, когда этот режим поднял туннель.
  - Умный VPN, «Конфигурации для обхода»: галочка вместо полоски у той, что везёт трафик. Снятие текущей — мгновенный переход на лучшую из оставшихся (`Selector.SetCandidates`), снятие последней — отключение, первая выбранная — подключение (селектор).
  - Новая плитка «Приложения под обход»: всё, что открывает приложение, идёт через туннель независимо от сайта. Windows — из запущенных процессов или по пути к .exe, сопоставление по имени exe через таблицы TCP/UDP (`desktop/apps_windows.go`). Android 10+ — выбор из установленных, владелец соединения через `getConnectionOwnerUid` (`AppRouting.kt`). Ядро: `AppRouterFunc` в netstack, `Engine.SetApps/TunnelApp`.
  - Не проверено руками на устройствах: только сборки и юнит-тесты. Следующий шаг — проверить на Windows и телефоне (приложения под обход, блокировку рекламы в приложении YouTube), затем стабильный 2.8.0. Тест `internal/linkguard` локально не запускается: антивирус блокирует тестовый бинарник.
- 2026-11-18 — заведены HISTORY.md и CLAUDE.md: правило вести историю и передавать работу между сессиями. Раздел «История» на дашборде — сделан 2026-11-25.

## Версии

Стабильные — без пометки, бета — с пометкой. Полные списки изменений — в описаниях релизов на GitHub.

- 2026-11-25 — **v2.8.5-beta.1** (beta) — IPv4 and IPv6 raced; pings over both
- 2026-11-19 — **v2.8.4**
- 2026-11-19 — **v2.8.4-beta.1** (beta) — island border and lighter blur on Android, twice as wide on Windows
- 2026-11-19 — **v2.8.3-beta.1** (beta) — the header scrolls with the page; full-width island on Android
- 2026-11-19 — **v2.8.2-beta.1** (beta) — floating blurred nav island, no hard edges top or bottom
- 2026-11-18 — **v2.8.1-beta.1** (beta) — all installed apps in the picker; Claude instead of Slack; catalogue sections
- 2026-11-18 — **v2.8.0-beta.1** (beta) — YouTube ad blocking, apps to route, "Подключено", config tap switches
- 2026-11-13 — **v2.7.3-beta.1** (beta)
- 2026-11-08 — **v2.7.2-beta.1** (beta)
- 2026-11-08 — **v2.7.1**
- 2026-11-08 — **v2.7.0-beta.3** (beta) — clients: blocks show with no config; "City, Country"; provider under the IP
- 2026-11-07 — **v2.7.0-beta.2** (beta) — release: build the server, keygen and relay without cgo
- 2026-11-07 — **v2.7.0-beta.1** (beta)
- 2026-11-05 — **v2.6.0**
- 2026-11-05 — **v2.5.3-beta.1** (beta)
- 2026-11-05 — **v2.5.2-beta.1** (beta)
- 2026-11-05 — **v2.5.1-beta.1** (beta)
- 2026-11-05 — **v2.5.0-beta.3** (beta) — tunnel: make the OPEN-ordering test hook atomic
- 2026-11-05 — **v2.5.0-beta.2** (beta) — tunnel: a first Write racing the OPEN holdback no longer overtakes the OPEN
- 2026-11-05 — **v2.5.0-beta.1** (beta)
- 2026-11-03 — **v2.4.0** — 2.4.0
- 2026-11-03 — **v2.4.0-beta.1** (beta) — 2.4.0-beta.1
- 2026-11-03 — **v2.3.0-beta.2** (beta) — 2.3.0-beta.2
- 2026-11-03 — **v2.3.0-beta.1** (beta) — 2.3.0-beta.1
- 2026-11-03 — **v2.2.0-beta.1** (beta) — 2.2.0-beta.1
- 2026-09-26 — **v2.1.0-beta.1** (beta)
- 2026-09-26 — **v2.0.0** — set up your own server from the app, stable
- 2026-09-26 — **v1.31.0-beta.1** (beta) — apps: set up your own server from IP, login and password; dialog scrollbar fix
- 2026-09-26 — **v1.30.0** — browser extension, stable
- 2026-09-26 — **v1.29.0-beta.1** (beta) — browser extension: add the site in the current tab to Умный VPN
- 2026-09-24 — **v1.28.0-beta.1** (beta) — tunnel: a config switch can no longer leave the device offline
- 2026-09-24 — **v1.27.0** — backgrounds: bigger, denser stars/mesh/meteors/embers; smooth aurora and wave edges
- 2026-09-24 — **v1.26.0-beta.1** (beta) — netstack: a silent server no longer freezes the device's packet processing; nav-style update icon
- 2026-09-24 — **v1.25.0-beta.1** (beta) — android: network changes move the tunnel instead of rebuilding it; resolve the server around the VPN
- 2026-09-24 — **v1.24.0-beta.1** (beta) — clients: catalogue services as one tile in the site list; Telegram ranges, YouTube app hosts; windows: remove split tunneling
- 2026-09-24 — **v1.23.0-beta.1** (beta) — android: notification disconnect turns off the mode driving the tunnel; popular resources three across, ChatGPT/Gemini
- 2026-09-24 — **v1.22.0-beta.1** (beta) — clients: Firefox fingerprint by default, paced handshakes, split DNS in smart mode
- 2026-09-24 — **v1.21.0-beta.1** (beta) — clients: keep the log to the last 24h; diagnostics for "Умный VPN takes the whole internet down"
- 2026-09-22 — **v1.20.0-beta.1** (beta) — clients: config pings bypass the tunnel; routing shows the tile ping and stays editable
- 2026-09-22 — **v1.19.0-beta.1** (beta) — ci: accept prerelease tags and publish them as GitHub prereleases
- 2026-09-21 — **v1.18.0** — release: 1.18.0
- 2026-09-16 — **v1.17.0** — release: 1.17.0
- 2026-09-16 — **v1.16.0** — release: 1.16.0
- 2026-09-16 — **v1.15.0** — release: 1.15.0
- 2026-09-04 — **v1.14.0** — release: 1.14.0
- 2026-08-19 — **v1.13.0** — v1.13.0
- 2026-08-11 — **v1.12.0** — v1.12.0
- 2026-07-28 — **v1.11.0** — Release 1.11.0: automatic country detection, theming, and review follow-ups
- 2026-07-28 — **v1.10.0** — Release 1.10.0: wire v2, encrypted destinations, and seven correctness fixes
- 2026-07-18 — **v1.9.0** — Release 1.9.0: failover, forward-secure handshake, padding, i18n, geo-privacy
- 2026-07-17 — **v1.8.0** — Release 1.8.0: SOCKS5 UDP ASSOCIATE, proxy net-change recovery, server throttle + metrics
- 2026-07-16 — **v1.7.1** — Release 1.7.1: proxy resilience, anti-fingerprint & pool cleanup, tray/notification polish
- 2026-07-16 — **v1.7.0** — v1.7.0: independent per-config SOCKS5 proxy with editable port on both platforms
- 2026-07-12 — **v1.6.0** — v1.6.0: update-via-button on both platforms, self-healing tunnel, release keystore, Android nav polish
- 2026-07-11 — **v1.5.0** — v1.5.0: auto-reconnect on network change, faster dead-connection detection, cmd/vpn removed
- 2026-07-10 — **v1.4.0** — v1.4.0: Android resource-ping page, per-app split tunneling for Windows
- 2026-07-10 — **v1.3.0** — v1.3.0: auto-update, new logo, resource-reachability panel, single-instance guard
- 2026-07-10 — **v1.2.0** — v1.2.0: post-quantum ClientHello fingerprint, one-time geo-IP resolution, Android tile polish
- 2026-07-07 — **v1.1.0** — v1.1.0: multi-config tiles, live ping/geo, tray icon (Android + Windows)
- 2026-07-03 — **v1.0.0** — v1.0.0 - initial public release
