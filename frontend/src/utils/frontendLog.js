import { LogFrontend } from '../../wailsjs/go/main/App';

const MAX_LOG_LENGTH = 8000;

let bridgeInstalled = false;
let originalConsoleLog = console.log.bind(console);
let originalConsoleError = console.error.bind(console);

function normalizeForJSON(value) {
  if (value instanceof Error) {
    return {
      name: value.name,
      message: value.message,
      stack: value.stack
    };
  }
  return value;
}

function stringifyValue(value) {
  if (typeof value === 'string') {
    return value;
  }
  try {
    return JSON.stringify(value, (_key, current) => normalizeForJSON(current));
  } catch (_error) {
    return String(value);
  }
}

function normalizeErrorLike(value) {
  if (value instanceof Error) {
    return `${value.name}: ${value.message}${value.stack ? `\n${value.stack}` : ''}`;
  }
  return stringifyValue(value);
}

function trimMessage(message) {
  if (!message || message.length <= MAX_LOG_LENGTH) {
    return message;
  }
  return `${message.slice(0, MAX_LOG_LENGTH)}...(truncated)`;
}

function sendToBackend(level, source, message) {
  const finalMessage = trimMessage(message);
  if (!finalMessage) {
    return;
  }
  Promise.resolve(LogFrontend(level, source, finalMessage)).catch(() => {});
}

export function logFrontend(scope, message, payload = null, isError = false) {
  const serialized = payload === null ? '' : ` ${stringifyValue(payload)}`;
  const line = `[${scope}] ${message}${serialized}`;

  if (isError) {
    originalConsoleError(line);
    sendToBackend('error', scope, line);
  } else {
    originalConsoleLog(line);
    sendToBackend('info', scope, line);
  }

  return line;
}

export function installGlobalFrontendLogBridge(app = null) {
  if (!bridgeInstalled) {
    bridgeInstalled = true;

    const currentConsoleError = console.error.bind(console);
    originalConsoleError = currentConsoleError;
    originalConsoleLog = console.log.bind(console);

    console.error = (...args) => {
      currentConsoleError(...args);
      sendToBackend('error', 'console.error', args.map(normalizeErrorLike).join(' '));
    };

    window.addEventListener('error', (event) => {
      const details = event.error
        ? normalizeErrorLike(event.error)
        : `${event.message || 'Unknown error'} @ ${event.filename || '<unknown>'}:${event.lineno || 0}:${event.colno || 0}`;
      sendToBackend('error', 'window.error', details);
    });

    window.addEventListener('unhandledrejection', (event) => {
      sendToBackend('error', 'unhandledrejection', normalizeErrorLike(event.reason));
    });
  }

  if (app?.config) {
    const previousHandler = app.config.errorHandler;
    app.config.errorHandler = (err, instance, info) => {
      sendToBackend(
        'error',
        'vue.errorHandler',
        `${info || 'Vue render error'} ${normalizeErrorLike(err)}`
      );
      if (typeof previousHandler === 'function') {
        previousHandler(err, instance, info);
      } else {
        originalConsoleError('[VueError]', err, info, instance);
      }
    };
  }
}
