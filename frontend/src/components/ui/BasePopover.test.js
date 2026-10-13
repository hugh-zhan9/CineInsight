import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import { nextTick } from 'vue';

import BaseModal from './BaseModal.vue';
import BasePopover from './BasePopover.vue';

// 菜单与浮层排在对话框之上（--z-menu 高于 --z-modal），所以对话框一打开，开着的浮层要自己收起，
// 不能盖住确认框、拦下给它的点击；焦点也不还给对话框后面的触发器。
const Host = {
  components: { BaseModal, BasePopover },
  data: () => ({ popover: false, modal: false }),
  template: `<div>
    <button type="button" class="btn" data-test="trigger" ref="trigger">筛选</button>
    <BasePopover v-if="popover" :anchor="$refs.trigger" @close="popover = false"><p>浮层</p></BasePopover>
    <BaseModal v-if="modal" data-test="modal" @close="modal = false">
      <input data-test="modal-input" />
      <BasePopover v-if="popover" @close="popover = false"><p>对话框里的浮层</p></BasePopover>
    </BaseModal>
  </div>`
};

const wrappers = [];
function mountHost() {
  const wrapper = mount(Host, { attachTo: document.body });
  wrappers.push(wrapper);
  return wrapper;
}

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  document.body.innerHTML = '';
});

describe('BasePopover 与对话框', () => {
  it('对话框打开时，开着的浮层收起，焦点不还给对话框后面的触发器', async () => {
    const wrapper = mountHost();
    wrapper.get('[data-test="trigger"]').element.focus();
    await wrapper.setData({ popover: true });
    await nextTick();
    expect(document.body.querySelector('.base-popover')?.textContent).toBe('浮层');

    await wrapper.setData({ modal: true });
    await nextTick();
    await nextTick();
    expect(wrapper.vm.popover).toBe(false);
    expect(document.body.querySelector('.base-popover')).toBeNull();
    expect(document.activeElement).not.toBe(wrapper.get('[data-test="trigger"]').element);
  });

  it('对话框已经开着时，在里面点开的浮层不受影响', async () => {
    const wrapper = mountHost();
    await wrapper.setData({ modal: true });
    await nextTick();
    await wrapper.setData({ popover: true });
    await nextTick();
    expect(wrapper.vm.popover).toBe(true);
    expect([...document.body.querySelectorAll('.base-popover')].map(node => node.textContent)).toContain('对话框里的浮层');
  });
});
