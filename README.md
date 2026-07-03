# Phantom — VPN на настоящем HTTPS-сертификате

> Полный технический разбор протокола (формат кадров, криптография, дисгайз-хендшейк,
> известные ограничения) — в [PROTOCOL.md](PROTOCOL.md). Этот файл — практическое
> руководство по установке и использованию.

## Содержание

1. [Обзор](#обзор)
2. [Требования](#требования)
3. [Генерация ключей](#генерация-ключей)
4. [Установка сервера](#установка-сервера)
5. [Установка клиента](#установка-клиента)
6. [Использование](#использование)
7. [Конфигурация](#конфигурация)
8. [Сборка мобильного ядра и APK](#сборка-мобильного-ядра-и-apk)
9. [Устранение проблем](#устранение-проблем)

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

### Два клиента, одно ядро

- **Desktop (Windows/Linux)** — `client.exe`/`client-linux`, локальный SOCKS5 +
  HTTP CONNECT прокси, плюс Windows-менеджер `vpn.exe`.
- **Android** — приложение `Phantom` (`android/`), заворачивает весь трафик устройства
  через `VpnService`. Общее Go-ядро (`mobile/`) собирается через `gomobile bind` и
  архитектурно готово к сборке iOS-клиента тем же способом — см.
  [PROTOCOL.md §11](PROTOCOL.md#11-ios-portability-path-not-implemented-in-this-repo).

---

## Требования

- Свой домен (или поддомен) с возможностью прописать A-запись на IP сервера — нужен
  для настоящего сертификата Let's Encrypt
- VPS: Ubuntu 22.04+ / Debian 12+, root-доступ по SSH
- Открытые порты: **80/tcp** (только для выпуска/продления сертификата, VPN-трафик
  через него не идёт) и порт VPN (по умолчанию **8443/tcp**)

---

## Генерация ключей

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

## Установка сервера

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

### Windows / Linux (desktop)

```cmd
client.exe -config client.yaml
```

Клиент поднимает:
- SOCKS5 прокси на `127.0.0.1:1080`
- HTTP CONNECT прокси на `127.0.0.1:1081`

Либо через менеджер: скопировать `vpn.exe`, `client.exe`, `client.yaml` в одну папку,
запустить `vpn.exe`, ввести `on`.

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

### Android

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

### Логи

- `vpn.log` / `vpn-client.log` — Windows-менеджер (клиент)
- `journalctl -u phantom -f` — сервер
- Кнопка **Посмотреть лог** в приложении, либо `adb logcat | grep -i phantom` — Android
