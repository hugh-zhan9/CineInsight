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

  it('reports every overlay self-click as overlay-click, whether or not it closes', async () => {
    const inert = mount(BaseModal);
    await inert.find('.modal-overlay').trigger('click');
    await inert.find('.modal').trigger('click');
    expect(inert.emitted('overlay-click')).toHaveLength(1);
    expect(inert.emitted('close')).toBeUndefined();

    const closable = mount(BaseModal, { props: { closeOnOverlay: true } });
    await closable.find('.modal-overlay').trigger('click');
    expect(closable.emitted('overlay-click')).toHaveLength(1);
    expect(closable.emitted('close')).toHaveLength(1);
  });
});

describe('BaseModal Escape goes to the topmost dialog only', () => {
  const pressEscape = () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));

  it('two stacked dialogs: one Escape closes only the top one; after it goes away the next Escape reaches the lower one', () => {
    const bottom = mount(BaseModal, { attachTo: document.body });
    const top = mount(BaseModal, { attachTo: document.body });
    const windowEscape = vi.fn();
    window.addEventListener('keydown', windowEscape);

    pressEscape();
    expect(top.emitted('close')).toHaveLength(1);
    expect(bottom.emitted('close')).toBeUndefined();
    expect(windowEscape).not.toHaveBeenCalled();

    top.unmount();
    pressEscape();
    expect(bottom.emitted('close')).toHaveLength(1);
    bottom.unmount();
    window.removeEventListener('keydown', windowEscape);
  });

  it('a dialog nested in another dialog\'s content and shown together is the top one', () => {
    const Host = {
      components: { BaseModal },
      emits: ['outer', 'inner'],
      template: '<BaseModal @close="$emit(\'outer\')"><BaseModal @close="$emit(\'inner\')" /></BaseModal>'
    };
    const wrapper = mount(Host, { attachTo: document.body });
    pressEscape();
    expect(wrapper.emitted('inner')).toHaveLength(1);
    expect(wrapper.emitted('outer')).toBeUndefined();
    wrapper.unmount();
  });

  it('closing the lower dialog first leaves the top one on top', () => {
    const bottom = mount(BaseModal, { attachTo: document.body });
    const top = mount(BaseModal, { attachTo: document.body });
    bottom.unmount();
    pressEscape();
    expect(top.emitted('close')).toHaveLength(1);
    top.unmount();
  });
});
