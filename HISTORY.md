# История Phantom

Журнал версий и действий. **Новые записи — сверху.** Пополняется после каждой завершённой работы (правило в CLAUDE.md).

## Действия

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
- 2026-11-18 — заведены HISTORY.md и CLAUDE.md: правило вести историю и передавать работу между сессиями. Раздел «История» на дашборде (cmd/console) — в работе.

## Версии

- 2026-11-19 — **v2.8.4-beta.1** — градиентная рамка островка на Android, меньше размытия; на Windows островок вдвое шире
- 2026-11-19 — **v2.8.3-beta.1** — шапка прокручивается вместе со страницей; на Android островок снова во всю ширину
- 2026-11-19 — **v2.8.2-beta.1** — плавающий размытый островок навигации, без рамок сверху и снизу; на Windows островок по центру ~5 см
- 2026-11-18 — **v2.8.1-beta.1** — Android: все установленные приложения в «Приложения под обход»; каталог: Claude вместо Slack, разделы Соцсети/Музыка/ИИ
- 2026-11-18 — **v2.8.0-beta.1** — блокировка рекламы YouTube, «Приложения под обход», «Подключено» на кнопке лучшей, переключение конфигураций обхода по нажатию
- 2026-11-13|HEAD -> main, tag: v2.7.3-beta.1, origin/main, origin/HEAD|release 2.7.3-beta.1: version files carry the plain 2.7.3
- 2026-11-08 — **v2.7.2-beta.1** — release 2.7.2-beta.1: version files carry the plain 2.7.2
- 2026-11-08 — **v2.7.1** — release 2.7.1
- 2026-11-08 — **v2.7.0-beta.3** — clients: blocks show with no config; "City, Country"; provider under the IP
- 2026-11-07 — **v2.7.0-beta.2** — release: build the server, keygen and relay without cgo
- 2026-11-07 — **v2.7.0-beta.1** — release 2.7.0-beta.1: version files carry the plain 2.7.0
- 2026-11-05 — **v2.6.0** — release 2.6.0
- 2026-11-05 — **v2.5.3-beta.1** — release 2.5.3-beta.1: version files carry the plain 2.5.3
- 2026-11-05 — **v2.5.2-beta.1** — release 2.5.2-beta.1: version files carry the plain 2.5.2
- 2026-11-05 — **v2.5.1-beta.1** — release 2.5.1-beta.1: version files carry the plain 2.5.1
- 2026-11-05 — **v2.5.0-beta.3** — tunnel: make the OPEN-ordering test hook atomic
- 2026-11-05 — **v2.5.0-beta.2** — tunnel: a first Write racing the OPEN holdback no longer overtakes the OPEN
- 2026-11-05 — **v2.5.0-beta.1** — release 2.5.0-beta.1: version files carry the plain 2.5.0
- 2026-11-03 — **v2.4.0-beta.1** — release 2.4.0-beta.1: version files carry the plain 2.4.0
- 2026-11-03 — **v2.3.0-beta.2** — tunnel: make the coalescing test deterministic; release 2.3.0-beta.2
- 2026-11-03 — **v2.3.0-beta.1** — release 2.3.0-beta.1: version files carry the plain 2.3.0
- 2026-11-03 — **v2.2.0-beta.1** — release 2.2.0-beta.1: version files carry the plain 2.2.0
- 2026-09-26 — **v2.1.0-beta.1** — release 2.1.0-beta.1: version files carry the plain 2.1.0
- 2026-09-26 — **v2.0.0** — release 2.0.0: set up your own server from the app, stable
- 2026-09-26 — **v1.31.0-beta.1** — apps: set up your own server from IP, login and password; dialog scrollbar fix
- 2026-09-26 — **v1.30.0** — release 1.30.0: browser extension, stable
- 2026-09-26 — **v1.29.0-beta.1** — browser extension: add the site in the current tab to Умный VPN
- 2026-09-24 — **v1.28.0-beta.1** — tunnel: a config switch can no longer leave the device offline
- 2026-09-24 — **v1.27.0** — backgrounds: bigger, denser stars/mesh/meteors/embers; smooth aurora and wave edges
- 2026-09-24 — **v1.26.0-beta.1** — netstack: a silent server no longer freezes the device's packet processing; nav-style update icon
- 2026-09-24 — **v1.25.0-beta.1** — android: network changes move the tunnel instead of rebuilding it; resolve the server around the VPN
- 2026-09-24 — **v1.24.0-beta.1** — clients: catalogue services as one tile in the site list; Telegram ranges, YouTube app hosts; windows: remove split tunneling
- 2026-09-24 — **v1.23.0-beta.1** — android: notification disconnect turns off the mode driving the tunnel; popular resources three across, ChatGPT/Gemini
- 2026-09-24 — **v1.22.0-beta.1** — clients: Firefox fingerprint by default, paced handshakes, split DNS in smart mode
- 2026-09-24 — **v1.21.0-beta.1** — clients: keep the log to the last 24h; diagnostics for "Умный VPN takes the whole internet down"
- 2026-09-22 — **v1.20.0-beta.1** — clients: config pings bypass the tunnel; routing shows the tile ping and stays editable
- 2026-09-22 — **v1.19.0-beta.1** — ci: accept prerelease tags and publish them as GitHub prereleases
- 2026-09-21 — **v1.18.0** — release: 1.18.0
- 2026-09-16 — **v1.17.0** — release: 1.17.0
- 2026-09-16 — **v1.16.0** — release: 1.16.0
- 2026-09-16 — **v1.15.0** — release: 1.15.0
- 2026-09-04 — **v1.14.0** — release: 1.14.0
- 2026-08-19 — **v1.13.0** — release: 1.13.0
- 2026-08-11 — **v1.12.0** — release: 1.12.0
- 2026-07-28 — **v1.11.0** — Merge pull request #2 from klion-gh/review-followups
- 2026-07-28 — **v1.10.0** — Merge pull request #1 from klion-gh/protocol-v2-hardening
- 2026-07-18 — **v1.9.0** — Release 1.9.0: failover, forward-secure handshake, padding, i18n, geo-privacy
- 2026-07-17 — **v1.8.0** — Release 1.8.0: SOCKS5 UDP ASSOCIATE, proxy net-change recovery, server throttle + metrics
- 2026-07-16 — **v1.7.1** — Release 1.7.1: proxy resilience, anti-fingerprint & pool cleanup, tray/notification polish
- 2026-07-16 — **v1.7.0** — Independent per-config SOCKS5 proxy with editable port on both platforms (v1.7.0)
- 2026-07-12 — **v1.6.0** — Update-via-button on both platforms, self-healing tunnel, Android nav polish (v1.6.0)
- 2026-07-11 — **v1.5.0** — Auto-reconnect on network change, faster dead-connection detection (v1.5.0)
- 2026-07-10 — **v1.4.0** — Android resource-ping page, per-app split tunneling for Windows (v1.4.0)
- 2026-07-10 — **v1.3.0** — Auto-update, new logo, resource-reachability panel, single-instance guard (v1.3.0)
- 2026-07-10 — **v1.2.0** — Bump version to 1.2.0
- 2026-07-07 — **v1.1.0** — Merge origin/main (README edit) into main
- 2026-07-03 — **v1.0.0** — Initial commit: Phantom VPN
