// 选项页。设置项的合法区间在 common/settings.js 里定义，这里只负责表单读写。

import { MSG } from '../common/constants.js';

const fields = {
  concurrency: document.getElementById('concurrency'),
  retries: document.getElementById('retries'),
  chunkSizeMB: document.getElementById('chunkSizeMB'),
  bridgeToken: document.getElementById('bridgeToken'),
  attachCookieOnPush: document.getElementById('attachCookieOnPush'),
  captureByDefault: document.getElementById('captureByDefault'),
  showUndownloadable: document.getElementById('showUndownloadable')
};
const messageEl = document.getElementById('message');
const bridgeEl = document.getElementById('bridge-status');

document.getElementById('save').addEventListener('click', save);

load().catch((error) => showMessage(error.message, true));

async function send(message) {
  const response = await chrome.runtime.sendMessage(message);
  if (!response) throw new Error('后台没有响应，试着重载扩展');
  if (!response.ok) throw new Error(response.error);
  return response.data;
}

async function load() {
  const settings = await send({ type: 'get-settings' });
  fields.concurrency.value = settings.concurrency;
  fields.retries.value = settings.retries;
  fields.chunkSizeMB.value = Math.round(settings.chunkSize / (1024 * 1024));
  fields.bridgeToken.value = settings.bridgeToken;
  fields.attachCookieOnPush.checked = settings.attachCookieOnPush;
  fields.captureByDefault.checked = settings.captureByDefault;
  fields.showUndownloadable.checked = settings.showUndownloadable;
  await refreshBridge();
}

async function save() {
  try {
    await send({
      type: 'save-settings',
      patch: {
        concurrency: Number(fields.concurrency.value),
        retries: Number(fields.retries.value),
        chunkSize: Number(fields.chunkSizeMB.value) * 1024 * 1024,
        bridgeToken: fields.bridgeToken.value.trim(),
        attachCookieOnPush: fields.attachCookieOnPush.checked,
        captureByDefault: fields.captureByDefault.checked,
        showUndownloadable: fields.showUndownloadable.checked
      }
    });
    // 存回去的是归一化之后的值，重新读一遍，免得界面上留着一个越界的数字。
    await load();
    showMessage('已保存');
  } catch (error) {
    showMessage(error.message, true);
  }
}

async function refreshBridge() {
  bridgeEl.textContent = '正在检测…';
  bridgeEl.classList.remove('ok', 'error');
  try {
    const state = await send({ type: MSG.PROBE_BRIDGE, force: true });
    bridgeEl.textContent = state.message;
    bridgeEl.classList.toggle('ok', Boolean(state.available));
  } catch (error) {
    bridgeEl.textContent = `检测失败：${error.message}`;
    bridgeEl.classList.add('error');
  }
}

function showMessage(text, isError = false) {
  messageEl.hidden = false;
  messageEl.textContent = text;
  messageEl.classList.toggle('error', Boolean(isError));
}
