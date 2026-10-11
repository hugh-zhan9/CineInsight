// v-reveal：区块滚动进入视口时淡入上移；绑定值是交错序号（每级 70ms）。
// 没有 IntersectionObserver 或用户偏好减少动态效果时直接显示，不做任何动画。
import { prefersReducedMotion } from '../../utils/recap.js';

let observer = null;

function sharedObserver() {
  if (observer || typeof IntersectionObserver !== 'function') return observer;
  observer = new IntersectionObserver(entries => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue;
      entry.target.classList.add('is-revealed');
      observer.unobserve(entry.target);
    }
  }, { rootMargin: '0px 0px -6% 0px', threshold: 0.06 });
  return observer;
}

export const reveal = {
  mounted(el, binding) {
    el.classList.add('recap-reveal');
    const step = Number(binding.value);
    if (Number.isFinite(step) && step > 0) el.style.setProperty('--reveal-delay', `${Math.min(step, 12) * 70}ms`);
    const io = prefersReducedMotion() ? null : sharedObserver();
    if (!io) {
      el.classList.add('is-revealed');
      return;
    }
    io.observe(el);
  },
  unmounted(el) {
    observer?.unobserve(el);
  }
};
