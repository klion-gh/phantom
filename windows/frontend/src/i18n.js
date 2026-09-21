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
    beta_updates: 'Скачивать beta-версии',
    beta_updates_hint: 'Предлагать обновления до тестовых сборок. Они выходят чаще, но могут содержать ошибки.',
    update_available_beta: 'Доступна beta-версия',
    routing: 'Маршрутизация',
    routing_mode: 'Режим',
    routing_mode_smart: 'Умный VPN',
    routing_mode_apps: 'По приложениям',
    smart_vpn: 'Умный VPN',
    smart_vpn_hint: 'Через VPN пойдут только перечисленные сайты. Остальной трафик — напрямую, как обычно.',
    smart_vpn_sites: 'Ресурсы под обход:',
    smart_vpn_site_ph: 'например, youtube.com',
    smart_vpn_no_sites_hint: 'Пока ничего не добавлено — VPN ведёт весь трафик',
    smart_vpn_apply_hint: 'Уже открытые вкладки продолжат идти по старому пути, пока не переподключитесь.',
    apply_changes: 'Применить',
    applying_changes: 'Применяем…',
    smart_vpn_configs: 'Конфигурации для обхода',
    smart_vpn_configs_hint: 'Выберите одну или несколько. При нескольких Phantom сам переключится на ту, что на связи.',
    popular_resources: 'Популярные ресурсы',
    auto_config: 'Выбирать лучшую',
    auto_config_picking: 'Подбираем сервер…',
    auto_config_hint: 'Автоматически направляет трафик через наиболее доступную конфигурацию.',
    auto_overrides_smart: 'Сейчас включено «Выбирать лучшую» — весь трафик идёт через VPN',
    smart_vpn_blocked_by_config: 'Выключите конфигурацию чтобы использовать этот режим.',
    smart_vpn_configs_warning: 'Выключите Умный VPN если необходимо включить конфигурацию для всего устройства.',
    no_configs_for_routing: 'Сначала добавьте конфигурацию',
    routing_active: 'активна',
    routing_checking: 'проверка',
    routing_unreachable: 'нет связи',
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
    background_stars_desc: 'Звёздные следы, как на выдержанном фото неба',
    background_mesh_label: 'Сеть',
    background_mesh_desc: 'Точки, соединённые тонкими линиями',
    background_meteors_label: 'Метеоры',
    background_meteors_desc: 'Редкие росчерки по диагонали',
    background_matrix_label: 'Матрица',
    background_matrix_desc: 'Падающие колонки светящихся 0 и 1',
    background_embers_label: 'Искры',
    background_embers_desc: 'Огоньки, поднимающиеся снизу вверх',
    background_plain_label: 'Без анимации',
    background_plain_desc: 'Только фоновый градиент',
    // log
    log: 'Лог',
    copy: 'Скопировать',
    // split tunneling
    apps: 'Приложения',
    apps_manage: 'Настроить',
    apps_direction: 'Включить / Исключить',
    apps_direction_include: 'Через VPN идут только указанные приложения',
    apps_direction_exclude: 'Через VPN идёт всё, кроме указанных приложений',
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
    beta_updates: 'Download beta versions',
    beta_updates_hint: 'Offer updates to test builds. They ship more often, but may contain bugs.',
    update_available_beta: 'A beta version is available',
    routing: 'Routing',
    routing_mode: 'Mode',
    routing_mode_smart: 'Smart VPN',
    routing_mode_apps: 'Per app',
    smart_vpn: 'Smart VPN',
    smart_vpn_hint: 'Only the listed sites go through the VPN. Everything else keeps its normal path.',
    smart_vpn_sites: 'Resources to route:',
    smart_vpn_site_ph: 'e.g. youtube.com',
    smart_vpn_no_sites_hint: 'Nothing added yet — the VPN carries all traffic',
    smart_vpn_apply_hint: 'Tabs already open will keep using the old path until you reconnect.',
    apply_changes: 'Apply',
    applying_changes: 'Applying…',
    smart_vpn_configs: 'Configurations to route through',
    smart_vpn_configs_hint: 'Pick one or more. With several, Phantom switches to whichever one is reachable.',
    popular_resources: 'Popular resources',
    auto_config: 'Best available',
    auto_config_picking: 'Picking a server…',
    auto_config_hint: 'Automatically routes traffic through whichever configuration is most reachable.',
    auto_overrides_smart: '“Best available” is on — all traffic goes through the VPN',
    smart_vpn_blocked_by_config: 'Turn off the configuration to use this mode.',
    smart_vpn_configs_warning: 'Turn off Smart VPN if you need to enable a configuration for the whole device.',
    no_configs_for_routing: 'Add a configuration first',
    routing_active: 'active',
    routing_checking: 'checking',
    routing_unreachable: 'unreachable',
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
    background_stars_desc: 'Star trails, like a long-exposure night sky photo',
    background_mesh_label: 'Mesh',
    background_mesh_desc: 'Dots connected by thin lines',
    background_meteors_label: 'Meteors',
    background_meteors_desc: 'Occasional diagonal streaks',
    background_matrix_label: 'Matrix',
    background_matrix_desc: 'Falling columns of glowing 0s and 1s',
    background_embers_label: 'Embers',
    background_embers_desc: 'Sparks drifting upward',
    background_plain_label: 'No animation',
    background_plain_desc: 'Background gradient only',
    log: 'Log',
    copy: 'Copy',
    apps: 'Apps',
    apps_manage: 'Manage',
    apps_direction: 'Include / Exclude',
    apps_direction_include: 'Only the listed apps go through the VPN',
    apps_direction_exclude: 'Everything except the listed apps goes through the VPN',
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
