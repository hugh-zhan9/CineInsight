import { mount } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vitest';

import BaseModal from './BaseModal.vue';

describe('BaseModal', () => {
  it('renders overlay, modal and slot content with pass-through attrs on the modal node', () => {
    const wrapper = mount(BaseModal, {
      attrs: { class: 'cleanup-modal', role: 'dialog', style: 'max-width: 480px;' },
      slots: { default: '<p class="inner">内容</p>' }
    });
    const overlay = wrapper.find('.modal-overlay');
    const modal = wrapper.find('.modal');
    expect(overlay.exists()).toBe(true);
    expect(modal.exists()).toBe(true);
    expect(modal.classes()).toContain('cleanup-modal');
    expect(modal.attributes('role')).toBe('dialog');
    expect(modal.attributes('aria-modal')).toBe('true');
    expect(modal.attributes('style')).toContain('max-width: 480px');
    expect(modal.find('.inner').text()).toBe('内容');
  });

  it('emits close on overlay self-click only when close-on-overlay is set', async () => {
    const closable = mount(BaseModal, { props: { closeOnOverlay: true } });
    await closable.find('.modal-overlay').trigger('click');
    expect(closable.emitted('close')).toHaveLength(1);

    const inert = mount(BaseModal);
    await inert.find('.modal-overlay').trigger('click');
    expect(inert.emitted('close')).toBeUndefined();
  });

  it('does not emit close when the click lands inside the modal', async () => {
    const wrapper = mount(BaseModal, { props: { closeOnOverlay: true } });
    await wrapper.find('.modal').trigger('click');
    expect(wrapper.emitted('close')).toBeUndefined();
  });

  it('stops modal click propagation only when stop-modal-clicks is set', async () => {
    const outsideHandler = vi.fn();
    const stopping = mount(BaseModal, {
      props: { stopModalClicks: true },
      attachTo: document.body
    });
    document.addEventListener('click', outsideHandler);
    await stopping.find('.modal').trigger('click');
    expect(outsideHandler).not.toHaveBeenCalled();

    const bubbling = mount(BaseModal, { attachTo: document.body });
    await bubbling.find('.modal').trigger('click');
    expect(outsideHandler).toHaveBeenCalledTimes(1);

    document.removeEventListener('click', outsideHandler);
    stopping.unmount();
    bubbling.unmount();
  });
});
