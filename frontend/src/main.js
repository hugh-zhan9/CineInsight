import { createApp } from 'vue';
import App from './App.vue';
import './styles/tokens.css';
import './styles/components.css';
import { installGlobalFrontendLogBridge } from './utils/frontendLog.js';

const app = createApp(App);

installGlobalFrontendLogBridge(app);

app.mount('#app');
