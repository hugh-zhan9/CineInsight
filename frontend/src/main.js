import { createApp } from 'vue';
import App from './App.vue';
import { installGlobalFrontendLogBridge } from './utils/frontendLog.js';

const app = createApp(App);

installGlobalFrontendLogBridge(app);

app.mount('#app');
