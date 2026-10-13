import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';

import DialogShell from './DialogShell.vue';

const mounted = [];
function mountDialog({ props = {}, slots = {}, attrs = {} } = {}) {
  const wrapper = mount(DialogShell, {
    props: { title: '移到废纸篓', ...props },
    attrs: { 'data-test': 'trash-dialog', ...attrs },
    slots: { default: '<p class="body">3 个文件</p>', ...slots },
    attachTo: document.body
  });
  mounted.push(wrapper);
  return wrapper;
}
const pressEscape = () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));

afterEach(() => { while (mounted.length) mounted.pop().unmount(); });

describe('DialogShell', () => {
  it('builds on BaseModal: labelled dialog with an h2 title, × close and passthrough attrs', () => {
    const wrapper = mountDialog({ props: { size: 'lg' }, slots: { footer: '<button type="button">取消</button><button type="button">移到废纸篓</button>' } });
    const dialog = wrapper.get('[role="dialog"]');
    expect(wrapper.find('.modal-overlay').exists()).toBe(true);
    expect(dialog.attributes('aria-modal')).toBe('true');
    expect(dialog.attributes('data-test')).toBe('trash-dialog');
    expect(dialog.classes()).toEqual(expect.arrayContaining(['modal', 'ui-dialog', 'ui-dialog--lg']));
    const title = dialog.get('h2');
    expect(title.text()).toBe('移到废纸篓');
    expect(dialog.attributes('aria-labelledby')).toBe(title.attributes('id'));
    expect(dialog.get('[data-test="dialog-shell-close"]').attributes('aria-label')).toBe('关闭');
    expect(dialog.get('.ui-dialog__body .body').text()).toBe('3 个文件');
    expect(dialog.findAll('.ui-dialog__footer button').map(button => button.text())).toEqual(['取消', '移到废纸篓']);
    expect(DialogShell.props.size.validator('huge')).toBe(false);
  });

  it('× and Esc always close', async () => {
    const wrapper = mountDialog();
    await wrapper.get('[data-test="dialog-shell-close"]').trigger('click');
    pressEscape();
    expect(wrapper.emitted('close')).toHaveLength(2);
    expect(wrapper.find('.ui-dialog__footer').exists()).toBe(false);
  });

  it('busy: Esc, × and the overlay are ignored and a spinner shows beside the title', async () => {
    const wrapper = mountDialog({ props: { busy: true } });
    expect(wrapper.get('.ui-dialog__head [role="status"]').attributes('aria-label')).toBe('正在处理');
    expect(wrapper.get('[role="dialog"]').attributes('aria-busy')).toBe('true');
    expect(wrapper.get('[data-test="dialog-shell-close"]').attributes('disabled')).toBeDefined();
    pressEscape();
    await wrapper.get('.modal-overlay').trigger('click');
    expect(wrapper.emitted('close')).toBeUndefined();
    await wrapper.setProps({ busy: false });
    pressEscape();
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('by default the overlay closes a dialog without form controls but not one with a form', async () => {
    const plain = mountDialog();
    await plain.get('.ui-dialog').trigger('click');
    expect(plain.emitted('close')).toBeUndefined();
    await plain.get('.modal-overlay').trigger('click');
    expect(plain.emitted('close')).toHaveLength(1);

    const form = mountDialog({ slots: { default: '<label>名称 <input type="text" class="text-input"></label>' } });
    await form.get('.modal-overlay').trigger('click');
    expect(form.emitted('close')).toBeUndefined();
  });

  it('an explicit closeOnOverlay wins over the form check', async () => {
    const forced = mountDialog({ props: { closeOnOverlay: true }, slots: { default: '<textarea class="text-input"></textarea>' } });
    await forced.get('.modal-overlay').trigger('click');
    expect(forced.emitted('close')).toHaveLength(1);
    const blocked = mountDialog({ props: { closeOnOverlay: false } });
    await blocked.get('.modal-overlay').trigger('click');
    expect(blocked.emitted('close')).toBeUndefined();
  });

  it('moves focus into the dialog and gives it back on close', () => {
    const opener = document.createElement('button');
    document.body.appendChild(opener);
    opener.focus();
    const wrapper = mountDialog();
    expect(document.activeElement).toBe(wrapper.get('.ui-dialog').element);
    mounted.pop().unmount();
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });
});
