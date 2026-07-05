import './style.css';
import { Connect, Disconnect, Status, ReadLog, SaveConfig, LoadConfig } from '../wailsjs/go/main/App';

const screens = {
  main: document.getElementById('screen-main'),
  config: document.getElementById('screen-config'),
  log: document.getElementById('screen-log'),
};

function showScreen(name) {
  for (const key in screens) {
    screens[key].classList.toggle('hidden', key !== name);
  }
}

const powerBtn = document.getElementById('btn-power');
const statusText = document.getElementById('status-text');
const hintText = document.getElementById('hint-text');
const configTextarea = document.getElementById('config-textarea');
const logText = document.getElementById('log-text');

let currentYaml = '';
let state = 'idle'; // idle | connecting | connected | error
let pollHandle = null;

function setState(next, message) {
  state = next;
  powerBtn.className = 'power-btn ' + next;
  statusText.className = 'status-text ' + next;

  switch (next) {
    case 'idle':
      statusText.textContent = 'Отключено';
      break;
    case 'connecting':
      statusText.textContent = 'Подключение...';
      break;
    case 'connected':
      statusText.textContent = 'Подключено';
      break;
    case 'error':
      statusText.textContent = 'Ошибка подключения';
      break;
  }

  if (next === 'error' && message) {
    hintText.textContent = message;
    hintText.classList.remove('hidden');
  } else if (next === 'idle' && !currentYaml) {
    hintText.textContent = 'Нажмите ⚙ и вставьте client.yaml';
    hintText.classList.remove('hidden');
  } else {
    hintText.classList.add('hidden');
  }
}

function stopPolling() {
  if (pollHandle) {
    clearInterval(pollHandle);
    pollHandle = null;
  }
}

function startPolling() {
  stopPolling();
  pollHandle = setInterval(async () => {
    try {
      const raw = await Status();
      const s = JSON.parse(raw);
      if (s.connected && !s.alive) {
        // Session died on its own (server restart, network drop, etc).
        setState('error', 'Соединение потеряно');
        stopPolling();
      }
    } catch (e) {
      console.error(e);
    }
  }, 4000);
}

async function toggleConnection() {
  if (state === 'connecting') return;

  if (state === 'connected') {
    await Disconnect();
    stopPolling();
    setState('idle');
    return;
  }

  if (!currentYaml.trim()) {
    showScreen('config');
    return;
  }

  setState('connecting');
  try {
    const err = await Connect(currentYaml);
    if (err) {
      setState('error', err);
    } else {
      setState('connected');
      startPolling();
    }
  } catch (e) {
    setState('error', String(e));
  }
}

powerBtn.addEventListener('click', toggleConnection);

document.getElementById('btn-gear').addEventListener('click', () => {
  configTextarea.value = currentYaml;
  showScreen('config');
});
document.getElementById('btn-back-config').addEventListener('click', () => showScreen('main'));

document.getElementById('btn-save').addEventListener('click', async () => {
  currentYaml = configTextarea.value;
  await SaveConfig(currentYaml);
  if (state === 'idle') {
    hintText.classList.toggle('hidden', !!currentYaml.trim());
  }
  showScreen('main');
});

document.getElementById('btn-view-log').addEventListener('click', async () => {
  logText.textContent = await ReadLog();
  showScreen('log');
});
document.getElementById('btn-back-log').addEventListener('click', () => showScreen('config'));

document.getElementById('btn-copy-log').addEventListener('click', async () => {
  try {
    await navigator.clipboard.writeText(logText.textContent);
  } catch (e) {
    console.error(e);
  }
});

// Load any previously saved config on startup.
(async () => {
  try {
    currentYaml = (await LoadConfig()) || '';
  } catch (e) {
    console.error(e);
  }
  setState('idle');
})();
