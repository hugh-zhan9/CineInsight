import { createApp } from 'vue';
import ShortFeedApp from './ShortFeedApp.vue';
import '../styles/tokens.css';
// 统一按钮（.btn / .btn-icon / .btn-plain）在这里定义（D-MW-06）。
import '../styles/components.css';
import './short-feed.css';

createApp(ShortFeedApp).mount('#short-feed-app');
