// UI string translations for the Phantom Windows frontend. The app was
// originally Russian-only; English was added via a language toggle in Settings.
//
// Static markup carries data-i18n / data-i18n-title / data-i18n-placeholder
// attributes, applied in bulk by applyStaticTranslations(). Strings built
// dynamically in main.js call t(key, params) directly, where params fill
// {placeholder} slots. The chosen language is persisted on the Go side
// (App.GetLanguage / App.SetLanguage) so the tray menu is translated too, not
// just the WebView UI.

const dict = {
  ru: {
    // header / main
    settings: 'Настройки',
    update_available: 'Доступно обновление',
    configs: 'Конфигурации',
    add_config: 'Добавить конфигурацию',
    no_configs: 'Нет добавленной конфигурации',
    no_configs_hint: 'Нажмите + чтобы добавить client.yaml',
    resources: 'Доступность ресурсов',
    add_resource: 'Добавить ресурс',
    // config edit
    paste_yaml: 'Вставьте содержимое client.yaml целиком:',
    save: 'Сохранить',
    delete: 'Удалить',
    edit_config_title: 'Редактировать конфигурацию',
    // settings
    view_log: 'Посмотреть лог',
    version: 'Версия',
    palette: 'Палитра',
    background: 'Фон',
    split_tunnel: 'Раздельное туннелирование',
    show_proxy_settings: 'Отобразить настройки прокси',
    language: 'Язык',
    palette_midnight: 'Полночь',
    palette_emerald: 'Изумруд',
    palette_sunset: 'Закат',
    palette_ocean: 'Океан',
    palette_graphite: 'Графит',
    palette_sakura: 'Сакура',
    background_orbs_label: 'Сферы',
    background_orbs_desc: 'Плавно плывущие пятна света',
    background_aurora_label: 'Сияние',
    background_aurora_desc: 'Медленные цветные ленты',
    background_stars_label: 'Звёзды',
    background_stars_desc: 'Мерцающие точки на фоне',
    background_mesh_label: 'Сеть',
    background_mesh_desc: 'Точки, соединённые тонкими линиями',
    background_meteors_label: 'Метеоры',
    background_meteors_desc: 'Редкие росчерки по диагонали',
    background_waves_label: 'Волны',
    background_waves_desc: 'Слоистые волнистые линии',
    background_embers_label: 'Искры',
    background_embers_desc: 'Огоньки, поднимающиеся снизу вверх',
    background_plain_label: 'Без анимации',
    background_plain_desc: 'Только фоновый градиент',
    // log
    log: 'Лог',
    copy: 'Скопировать',
    // split tunneling
    apps: 'Приложения',
    add_app: 'Добавить приложение',
    split_tunnel_hint: 'Эти приложения будут работать напрямую, в обход VPN, даже при активном подключении.',
    empty_list: 'Список пуст',
    empty_apps_hint: 'Нажмите + чтобы выбрать .exe',
    // dialogs
    delete_config_q: 'Удалить конфигурацию?',
    delete_config_text: 'Придётся снова вставить client.yaml, чтобы подключиться этим профилем.',
    cancel: 'Отмена',
    add: 'Добавить',
    resource_name_ph: 'Название, например Netflix',
    // tiles / dynamic
    ping: 'Пинг',
    ms: 'мс',
    available: 'Доступен',
    unavailable: 'Недоступен',
    checking: 'Проверка...',
    remove: 'Удалить',
    edit: 'Редактировать',
    connect: 'Подключить',
    proxy_tooltip: 'Независимый SOCKS5-прокси',
    proxy_tooltip_active: 'Независимый SOCKS5-прокси — 127.0.0.1:{port}',
    port_ph: 'порт',
    proxy_port_title: 'Порт независимого прокси - редактируется, пока прокси выключен',
    bad_port: 'Некорректный порт: {port}',
    proxy_failed: 'Не удалось включить прокси на порту {port}: {error}',
    proxy_any: '(любой)',
    // update banner / status
    update_btn_title: 'Доступно обновление {tag} — нажмите, чтобы установить',
    update_banner_available: 'Доступно обновление {tag} — нажмите зелёную стрелку рядом с настройками, чтобы установить.',
    update_installing: 'Установка обновления {tag} — скачивание и перезапуск...',
    update_failed: 'Не удалось обновиться: {message}',
    reconnecting: 'Смена сети — переподключение...',
    connect_timeout: 'Не удалось подключиться: сервер не ответил вовремя',
  },
  en: {
    settings: 'Settings',
    update_available: 'Update available',
    configs: 'Configurations',
    add_config: 'Add configuration',
    no_configs: 'No configuration added',
    no_configs_hint: 'Tap + to add client.yaml',
    resources: 'Resource availability',
    add_resource: 'Add resource',
    paste_yaml: 'Paste the full client.yaml contents:',
    save: 'Save',
    delete: 'Delete',
    edit_config_title: 'Edit configuration',
    view_log: 'View log',
    version: 'Version',
    palette: 'Palette',
    background: 'Background',
    split_tunnel: 'Split tunneling',
    show_proxy_settings: 'Show proxy settings',
    language: 'Language',
    palette_midnight: 'Midnight',
    palette_emerald: 'Emerald',
    palette_sunset: 'Sunset',
    palette_ocean: 'Ocean',
    palette_graphite: 'Graphite',
    palette_sakura: 'Sakura',
    background_orbs_label: 'Orbs',
    background_orbs_desc: 'Softly drifting patches of light',
    background_aurora_label: 'Aurora',
    background_aurora_desc: 'Slow-moving coloured ribbons',
    background_stars_label: 'Stars',
    background_stars_desc: 'Twinkling points across the backdrop',
    background_mesh_label: 'Mesh',
    background_mesh_desc: 'Dots connected by thin lines',
    background_meteors_label: 'Meteors',
    background_meteors_desc: 'Occasional diagonal streaks',
    background_waves_label: 'Waves',
    background_waves_desc: 'Layered wavy lines',
    background_embers_label: 'Embers',
    background_embers_desc: 'Sparks drifting upward',
    background_plain_label: 'No animation',
    background_plain_desc: 'Background gradient only',
    log: 'Log',
    copy: 'Copy',
    apps: 'Apps',
    add_app: 'Add app',
    split_tunnel_hint: 'These apps will connect directly, bypassing the VPN, even while it is active.',
    empty_list: 'The list is empty',
    empty_apps_hint: 'Tap + to pick an .exe',
    delete_config_q: 'Delete configuration?',
    delete_config_text: 'You will have to paste client.yaml again to connect with this profile.',
    cancel: 'Cancel',
    add: 'Add',
    resource_name_ph: 'Name, e.g. Netflix',
    ping: 'Ping',
    ms: 'ms',
    available: 'Available',
    unavailable: 'Unavailable',
    checking: 'Checking...',
    remove: 'Remove',
    edit: 'Edit',
    connect: 'Connect',
    proxy_tooltip: 'Independent SOCKS5 proxy',
    proxy_tooltip_active: 'Independent SOCKS5 proxy — 127.0.0.1:{port}',
    port_ph: 'port',
    proxy_port_title: 'Independent proxy port - editable while the proxy is off',
    bad_port: 'Invalid port: {port}',
    proxy_failed: 'Couldn’t start the proxy on port {port}: {error}',
    proxy_any: '(any)',
    update_btn_title: 'Update {tag} available — click to install',
    update_banner_available: 'Update {tag} available — click the green arrow next to settings to install.',
    update_installing: 'Installing update {tag} — downloading and restarting...',
    update_failed: 'Update failed: {message}',
    reconnecting: 'Network changed — reconnecting...',
    connect_timeout: 'Connection failed: the server did not respond in time',
  },
};

let lang = 'ru';

export function getLang() {
  return lang;
}

export function setLang(l) {
  lang = l === 'en' ? 'en' : 'ru';
}

// t looks up key in the current language (falling back to Russian, then to the
// key itself) and fills any {name} slots from params.
export function t(key, params) {
  const table = dict[lang] || dict.ru;
  let s = table[key] !== undefined ? table[key] : dict.ru[key] !== undefined ? dict.ru[key] : key;
  if (params) {
    for (const k in params) {
      s = s.replace('{' + k + '}', params[k]);
    }
  }
  return s;
}

// applyStaticTranslations rewrites every element carrying a data-i18n*
// attribute under root for the current language. Called on load and whenever
// the language changes.
export function applyStaticTranslations(root = document) {
  root.querySelectorAll('[data-i18n]').forEach((el) => {
    el.textContent = t(el.getAttribute('data-i18n'));
  });
  root.querySelectorAll('[data-i18n-title]').forEach((el) => {
    el.title = t(el.getAttribute('data-i18n-title'));
  });
  root.querySelectorAll('[data-i18n-placeholder]').forEach((el) => {
    el.placeholder = t(el.getAttribute('data-i18n-placeholder'));
  });
}
