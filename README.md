# Phantom — VPN на настоящем HTTPS-сертификате

## Содержание

1. [Обзор](#обзор)
2. [Быстрая установка сервера (одна команда)](#быстрая-установка-сервера-одна-команда)
3. [Требования](#требования)
4. [Генерация ключей](#генерация-ключей)
5. [Установка сервера вручную](#установка-сервера-вручную)
6. [Установка клиента](#установка-клиента)
7. [Использование](#использование)
8. [Конфигурация](#конфигурация)
9. [Сборка мобильного ядра и APK](#сборка-мобильного-ядра-и-apk)
10. [Сборка Windows-приложения](#сборка-windows-приложения)
11. [Устранение проблем](#устранение-проблем)

---

## Обзор

Phantom — личный VPN, устроенный так, чтобы соединение с сервером было неотличимо от
обычного HTTPS-визита на настоящий сайт — потому что это и есть настоящий сайт: сервер
получает реальный сертификат Let's Encrypt на домен, которым управляет владелец, а не
самоподписанный. Аутентификация и обмен ключами спрятаны внутри запроса, который
выглядит как обычный HTTP-апгрейд до WebSocket; всё, что не прошло проверку, получает
настоящий сайт-заглушку вместо обрыва соединения.

### Ключевые особенности

- **Настоящий CA-сертификат** (Let's Encrypt, автовыпуск и автопродление) — не
  самоподписанный, поэтому обычная проверка цепочки сертификата ничего не находит
- **Forward secrecy** — свежий эфемерный X25519 ключ на КАЖДОЕ подключение (тот же
  принцип, что и в Reality/XTLS), а не статичный ключ из PSK
- **Маскировка хендшейка под WebSocket-апгрейд** — никакого характерного "одного
  фрейма фиксированного размера" сразу после TLS
- **Настоящий сайт-заглушка** — непрошедшие проверку получают код 200 и реальный
  сертификат вместо тишины и таймаута
- **TCP + UDP релей** — работает DNS, QUIC/HTTP3, WebRTC через туннель, не только TCP
- **Padding** — размер зашифрованного фрейма не выдаёт размер полезной нагрузки
- **Полноценный VPN на Android** — весь IP-трафик устройства через `VpnService`, а не
  только явно настроенные на прокси приложения

### Клиенты

- **Windows (`windows/`, Phantom.exe)** — полноценный системный VPN на Wintun (тот же
  драйвер, что использует WireGuard-для-Windows): весь IP-трафик машины идёт через
  туннель, а не только прокси-осведомлённые приложения. Тот же тёмный интерфейс с
  большой круглой кнопкой, что и в Android-приложении. Требует запуска от имени
  администратора (создание TUN-адаптера и изменение таблицы маршрутов).
- **Android** — приложение `Phantom` (`android/`), заворачивает весь трафик устройства
  через `VpnService`. Общее Go-ядро (`mobile/`) собирается через `gomobile bind` и
  архитектурно готово к сборке iOS-клиента тем же способом — см.
  [PROTOCOL.md §11](PROTOCOL.md#11-ios-portability-path-not-implemented-in-this-repo).

---

## Быстрая установка сервера (одна команда)

Сначала пропишите A-запись своего домена на IP сервера (см. [«Требования»](#требования))
и дождитесь распространения — без этого сертификат не выпустится. Дальше на самом VPS
(от root):

```bash
curl -fsSL https://raw.githubusercontent.com/klion-gh/phantom/main/scripts/install.sh | sh
```

Скрипт скачает готовый бинарник с [GitHub Releases](https://github.com/klion-gh/phantom/releases),
спросит домен (или возьмёт из `PHANTOM_DOMAIN=ваш-домен.ru`, если нужен неинтерактивный
запуск), сгенерирует ключи, поднимет systemd-сервис `phantom`, откроет порты 80/8443
через `ufw` (если он есть), и в конце выведет готовый `client.yaml` — просто скопируйте
его в приложение/клиент.

Повторный запуск той же команды на уже установленном сервере **обновляет бинарник**,
не трогая конфиг и ключи (проверено — это ровно то, что происходит при выходе новой
версии).

Удаление — та же команда с аргументом `uninstall`:

```bash
curl -fsSL https://raw.githubusercontent.com/klion-gh/phantom/main/scripts/install.sh | sh -s -- uninstall
```

Остановит и уберёт сервис, спросит (интерактивно, через `/dev/tty`), удалять ли
`/var/lib/phantom` — кэш уже выпущенного сертификата (полезно оставить, если планируете
переустановить с тем же доменом). `PHANTOM_PURGE=1` перед командой — удалить сразу без
вопроса.

Исходники скрипта: [`scripts/install.sh`](scripts/install.sh) — это обычный POSIX
`sh`, ничего скрытого; можно прочитать перед запуском.

---

## Требования

- Свой домен (или поддомен) с возможностью прописать A-запись на IP сервера — нужен
  для настоящего сертификата Let's Encrypt
- VPS: Ubuntu 22.04+ / Debian 12+, root-доступ по SSH
- Открытые порты: **80/tcp** (только для выпуска/продления сертификата, VPN-трафик
  через него не идёт) и порт VPN (по умолчанию **8443/tcp**)

---

## Генерация ключей

> Нужно только для ручной установки — быстрый скрипт выше уже генерирует ключи сам.

```bash
go build -o keygen.exe ./cmd/keygen
./keygen.exe
```

Вывод:
```
=== Phantom Key Material ===

Server Private Key: <64 hex chars>
Server Public Key:  <64 hex chars>
PSK:                <64 hex chars>

=== server.yaml ===
private_key: "..."
psk: "..."

=== client.yaml ===
server_public_key: "..."
psk: "..."
```

`Server Private Key` остаётся только на сервере. `Server Public Key` и `PSK` идут в
конфиг каждого клиента — `psk` должен быть **идентичен** на клиенте и сервере.

---

## Установка сервера вручную

> Если устраивает [быстрая установка](#быстрая-установка-сервера-одна-команда) выше —
> этот раздел можно пропустить. Он показывает то же самое по шагам, вручную.

### Шаг 1: DNS

Пропишите A-запись домена на IP вашего VPS и дождитесь распространения (проверить:
`nslookup ваш-домен.ru`).

### Шаг 2: Подключение и загрузка файлов

```bash
ssh root@<IP_СЕРВЕРА>

# На локальной машине:
GOOS=linux GOARCH=amd64 go build -o bin/server-linux ./cmd/server
scp bin/server-linux root@<IP>:/opt/phantom/phantom-server
scp configs/server.yaml root@<IP>:/opt/phantom/server.yaml
```

### Шаг 3: Права и конфиг

```bash
mkdir -p /opt/phantom /var/lib/phantom
chmod 755 /opt/phantom/phantom-server
```

Заполните `/opt/phantom/server.yaml` (см. [«Конфигурация»](#конфигурация)) — обязательно
укажите свой `domain` и ключи из шага генерации.

### Шаг 4: Открытие портов

```bash
ufw allow 80/tcp
ufw allow 8443/tcp
```

### Шаг 5: systemd-сервис

```bash
cat > /etc/systemd/system/phantom.service << 'EOF'
[Unit]
Description=Phantom VPN Server
After=network.target

[Service]
Type=simple
ExecStart=/opt/phantom/phantom-server -config /opt/phantom/server.yaml
WorkingDirectory=/opt/phantom
Restart=on-failure
RestartSec=5
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable phantom
systemctl start phantom
```

`AmbientCapabilities=CAP_NET_BIND_SERVICE` нужен, только если `listen`/ACME-порт ниже
1024 без запуска от root; при обычном запуске от root можно убрать.

### Шаг 6: Проверка

```bash
systemctl status phantom
journalctl -u phantom -f
```

При первом реальном TLS-подключении к порту из `listen` сервер лениво запросит
сертификат у Let's Encrypt через порт 80 — в логе должно появиться подтверждение
выдачи. Проверить сертификат снаружи:

```bash
echo | openssl s_client -connect ваш-домен.ru:8443 -servername ваш-домен.ru 2>/dev/null | openssl x509 -noout -issuer -subject -dates
```

Должно быть `issuer=... O=Let's Encrypt ...` и `subject=CN=ваш-домен.ru`.

### Обновление уже установленного сервера

```bash
GOOS=linux GOARCH=amd64 go build -o bin/server-linux ./cmd/server
scp bin/server-linux root@<IP>:/opt/phantom/phantom-server
ssh root@<IP> "systemctl restart phantom"
```

Кэш выпущенного сертификата (`/var/lib/phantom/acme`) переживает перезапуск и
обновление бинарника — повторный запрос к Let's Encrypt не потребуется.

---

## Установка клиента

### Windows / Linux (desktop-прокси)

```cmd
client.exe -config client.yaml
```

Клиент поднимает:
- SOCKS5 прокси на `127.0.0.1:1080`
- HTTP CONNECT прокси на `127.0.0.1:1081`

Либо через менеджер: скопировать `vpn.exe`, `client.exe`, `client.yaml` в одну папку,
запустить `vpn.exe`, ввести `on`.

### Windows (Phantom.exe, полноценный VPN)

1. Собрать (см. [«Сборка Windows-приложения»](#сборка-windows-приложения)) или взять
   готовый `phantom.exe` из `bin/`.
2. Запустить **от имени администратора** — без этого создание TUN-адаптера и правка
   таблицы маршрутов не сработают, приложение покажет ошибку.
3. Нажать ⚙, вставить содержимое своего `client.yaml` целиком, нажать **Сохранить**.
4. Вернуться назад, нажать большую круглую кнопку.
5. Статус сменится на «Подключено» — весь IP-трафик машины пойдёт через туннель.

Лог (`phantom.log`) пишется рядом с `phantom.exe` — там видно каждую команду
`netsh`/`route`, которую приложение выполняет при подключении/отключении, и её вывод.
Кнопка **Посмотреть лог** в приложении показывает то же самое.

> Если на машине уже есть другой активный VPN/WireGuard-клиент, при подключении Phantom
> он может ненадолго переподключиться сам (его собственная защита от утечек реагирует
> на изменение таблицы маршрутов) — это нормально, не баг Phantom.

### Android

1. Установить `bin/phantom.apk` (`adb install -r bin/phantom.apk` или перекинуть файл).
2. Открыть приложение **Phantom**.
3. Нажать ⚙, вставить содержимое своего `client.yaml` целиком, нажать **Сохранить**.
4. Вернуться назад, нажать большую круглую кнопку — подтвердить системный диалог
   согласия на VPN при первом запуске.
5. Статус сменится на «Подключено» — весь трафик устройства пойдёт через туннель.

Диагностика без ADB: кнопка **Посмотреть лог** в конфиг-экране показывает файловый лог
и умеет отправить его через любое приложение.

---

## Использование

### Desktop: через curl / FoxyProxy / Telegram

```cmd
curl --proxy socks5://127.0.0.1:1080 https://ifconfig.me
curl --proxy http://127.0.0.1:1081 https://ifconfig.me
```

FoxyProxy / Telegram: тип SOCKS5, адрес `127.0.0.1`, порт `1080`.

### Windows (Phantom.exe) и Android

Ручная настройка прокси не нужна — после подключения весь трафик уже идёт через
туннель на уровне системы.

---

## Конфигурация

Один и тот же `client.yaml` подходит и для desktop, и для Android (на Android его
просто вставляют в текстовое поле целиком).

### Сервер (server.yaml)

```yaml
listen: ":8443"                       # порт VPN — независим от ACME (см. PROTOCOL.md §6.2)
domain: "ваш-домен.ru"                # обязателен, должен резолвиться на этот сервер
acme_email: ""                        # необязательно — контактный email для Let's Encrypt
acme_cache_dir: "/var/lib/phantom/acme"  # где хранится выпущенный сертификат
private_key: "..."                    # X25519 приватный ключ сервера (из keygen)
psk: "..."                            # общий секрет, должен совпадать с клиентами
decoy_site_dir: ""                    # папка со статикой для сайта-заглушки; пусто = встроенная страница
log_level: "debug"
```

### Клиент (client.yaml)

```yaml
server: "ваш-домен.ru:8443"           # адрес:порт сервера
domain: "ваш-домен.ru"                # тот же домен — используется как SNI
fingerprint: "chrome120"              # chrome120 / firefox120 / safari16
psk: "..."                            # должен совпадать с сервером
server_public_key: "..."              # X25519 публичный ключ сервера (из keygen)
listen: "127.0.0.1:1080"              # SOCKS5 (только desktop)
listen_http: "127.0.0.1:1081"         # HTTP CONNECT (только desktop)
pool_size: 4
log_level: "debug"
```

---

## Сборка мобильного ядра и APK

Нужны: Go 1.26+, JDK 17, Android SDK (`platform-tools`, `platforms;android-34`,
`build-tools;34.0.0`, NDK), `gomobile`/`gobind`, Gradle.

```bash
gomobile bind -target=android -androidapi 24 -o android/app/libs/mobile.aar ./mobile

cd android
./gradlew assembleDebug
# результат: android/app/build/outputs/apk/debug/app-debug.apk
```

Детали gVisor-обвязки, `Protector`/`VpnService.protect()` и устройство Kotlin-приложения
— в [PROTOCOL.md §9-10](PROTOCOL.md#9-mobile-core-mobilemobilego).

---

## Сборка Windows-приложения

Нужны: Go 1.26+, Node.js/npm, [Wails CLI](https://wails.io) (`go install
github.com/wailsapp/wails/v2/cmd/wails@latest`).

```bash
cd windows
wails build
# результат: windows/build/bin/phantom.exe
```

`wintun.dll` (драйвер Wintun) встроен в бинарник через `//go:embed` и распаковывается
рядом с `phantom.exe` при первом запуске — распространяется один-единственный `.exe`.

Манифест (`windows/build/windows/wails.exe.manifest`) уже настроен на
`requireAdministrator` — Windows сама покажет запрос UAC при запуске.

---

## Устранение проблем

### Сертификат не выпускается / сервер не стартует

**Проверить**: DNS домена реально указывает на IP этого сервера, порт 80 открыт и
ничем не занят (`ss -tlnp | grep :80`), в логе (`journalctl -u phantom -f`) смотреть
ошибки ACME.

### "auth failed" / бесконечное "connecting"

**Причина**: `psk` не совпадает между клиентом и сервером, либо `domain` в client.yaml
не совпадает с реальным доменом сертификата сервера.

### TLS handshake error

```cmd
curl -v https://ваш-домен.ru:8443/
```

Должен вернуться **HTTP 200** с содержимым сайта-заглушки и **без** предупреждений о
сертификате (это не VPN-трафик, а нормальный HTTP-запрос — так и должно быть: сервер
отвечает как обычный сайт всем, кто не прошёл встроенную проверку). Если вместо этого
таймаут или `Connection refused` — порт закрыт или сервис не запущен.

### Android: приложение не открывается / вылетает

Кнопка **Посмотреть лог** в приложении покажет файловый лог даже после краша, либо:
```
adb logcat | grep -i phantom
```

### Windows-приложение: ошибка при подключении / нет прав

**"are you running as Administrator?"** — приложение не запущено от администратора;
создание TUN-адаптера и правка таблицы маршрутов требуют повышенных прав. Запустить
`phantom.exe` через «Запуск от имени администратора».

### Windows-приложение: IP не меняется после подключения

Если на этом же VPS дополнительно висит другой VPN (например, Amnezia/WireGuard),
внешний IP не покажет разницы — оба выходят через один и тот же сервер. Проверять по
внешнему IP имеет смысл, только если Phantom — единственный активный туннель.

### Логи

- `vpn.log` / `vpn-client.log` — Windows-менеджер (desktop-прокси клиент)
- `phantom.log` рядом с `phantom.exe` — Windows-приложение (полноценный VPN)
- `journalctl -u phantom -f` — сервер
- Кнопка **Посмотреть лог** в приложении, либо `adb logcat | grep -i phantom` — Android
