import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vitest';

import BaseMenu from './BaseMenu.vue';
import BasePopover from './BasePopover.vue';

const ITEMS = [
  { heading: '扫描' },
  { id: 'scan-new', label: '扫描新目录', shortcut: '⇧⌘N' },
  { id: 'scan-inc', label: '增量扫描', shortcut: '⌘R' },
  { heading: '维护' },
  { id: 'trash', label: '回收站', disabled: true },
  { divider: true },
  { id: 'delete', label: '删除', danger: true }
];

function mountMenu(props = {}) {
  return mount(BaseMenu, { props: { items: ITEMS, ...props }, attachTo: document.body });
}

function menuItems() {
  return Array.from(document.querySelectorAll('.base-menu__item'));
}

// Teleport 把浮层挂到 body，wrapper.find 到不了，只能查真实 DOM 派发原生事件。
function key(selector, k) {
  document.querySelector(selector).dispatchEvent(
    new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true })
  );
}

function mouseDown(el) {
  el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
}

afterEach(() => {
  document.body.innerHTML = '';
});

describe('BaseMenu', () => {
  it('renders headings, dividers, shortcuts and disabled state', () => {
    const wrapper = mountMenu();
    expect(document.querySelectorAll('.base-menu__heading')).toHaveLength(2);
    expect(document.querySelectorAll('.base-menu__divider')).toHaveLength(1);
    const items = menuItems();
    expect(items.map(el => el.dataset.menuId)).toEqual(['scan-new', 'scan-inc', 'trash', 'delete']);
    expect(items[0].querySelector('.base-menu__shortcut').textContent).toBe('⇧⌘N');
    expect(items[2].disabled).toBe(true);
    expect(items[3].className).toContain('base-menu__item--danger');
    wrapper.unmount();
  });

  it('方向键只在可选项间移动，跳过标题、分隔线和禁用项', async () => {
    const wrapper = mountMenu();

    key('.base-menu__list', 'ArrowDown');
    expect(wrapper.vm.activeIndex).toBe(1);
    key('.base-menu__list', 'ArrowDown');
    expect(wrapper.vm.activeIndex).toBe(2);
    // 索引 3 是标题、4 是禁用项、5 是分隔线，下一步必须直接落到 6。
    key('.base-menu__list', 'ArrowDown');
    expect(wrapper.vm.activeIndex).toBe(6);
    // 到底回绕。
    key('.base-menu__list', 'ArrowDown');
    expect(wrapper.vm.activeIndex).toBe(1);
    key('.base-menu__list', 'ArrowUp');
    expect(wrapper.vm.activeIndex).toBe(6);
    wrapper.unmount();
  });

  it('Enter 选中当前项并关闭；点击也一样', async () => {
    const wrapper = mountMenu();
    key('.base-menu__list', 'ArrowDown');
    key('.base-menu__list', 'Enter');
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted('select')[0][0].id).toBe('scan-new');
    expect(wrapper.emitted('close')).toHaveLength(1);
    wrapper.unmount();

    const clicked = mountMenu();
    menuItems()[1].click();
    await clicked.vm.$nextTick();
    expect(clicked.emitted('select')[0][0].id).toBe('scan-inc');
    expect(clicked.emitted('close')).toHaveLength(1);
    clicked.unmount();
  });

  it('禁用项既不能被点中也不会被 Enter 选中', async () => {
    const wrapper = mountMenu();
    wrapper.vm.activeIndex = 4;
    await wrapper.vm.$nextTick();
    key('.base-menu__list', 'Enter');
    expect(wrapper.emitted('select')).toBeUndefined();
    wrapper.unmount();
  });

  it('方向键和回车不会冒泡出去，列表快捷键在菜单打开时不会同时触发', async () => {
    const outer = vi.fn();
    document.addEventListener('keydown', outer);
    const wrapper = mountMenu();
    key('.base-menu__list', 'ArrowDown');
    key('.base-menu__list', 'Enter');
    expect(outer).not.toHaveBeenCalled();
    document.removeEventListener('keydown', outer);
    wrapper.unmount();
  });
});

describe('BasePopover', () => {
  it('Esc 关闭，且不让 Esc 继续往外冒泡', async () => {
    const outer = vi.fn();
    document.addEventListener('keydown', outer);
    const wrapper = mount(BasePopover, { attachTo: document.body });
    key('.base-popover', 'Escape');
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted('close')).toHaveLength(1);
    expect(outer).not.toHaveBeenCalled();
    document.removeEventListener('keydown', outer);
    wrapper.unmount();
  });

  it('点浮层内部不关闭，点外部关闭', async () => {
    const wrapper = mount(BasePopover, { attachTo: document.body });
    mouseDown(document.querySelector('.base-popover'));
    expect(wrapper.emitted('close')).toBeUndefined();

    mouseDown(document.body);
    expect(wrapper.emitted('close')).toHaveLength(1);
    wrapper.unmount();
  });

  it('点在触发元素上不算点外部，否则按钮会立刻把刚关掉的浮层再打开', async () => {
    const trigger = document.createElement('button');
    document.body.appendChild(trigger);
    const wrapper = mount(BasePopover, { props: { anchor: trigger }, attachTo: document.body });
    mouseDown(trigger);
    expect(wrapper.emitted('close')).toBeUndefined();
    wrapper.unmount();
    trigger.remove();
  });

  // 片库的全局快捷键处理用 document.querySelector('[role="dialog"]') 判断
  // "有浮层开着就别响应"。浮层带这个 role 是快捷键挂起的唯一依据，去掉它
  // 会让菜单打开时 J/K/F/W/T 同时生效。
  it('带 role="dialog"，列表快捷键据此自动挂起', () => {
    const wrapper = mount(BasePopover, { attachTo: document.body });
    expect(document.querySelector('.base-popover').getAttribute('role')).toBe('dialog');
    expect(document.querySelector('[role="dialog"]')).toBeTruthy();
    wrapper.unmount();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it('关闭后把焦点还给打开它的元素', async () => {
    const trigger = document.createElement('button');
    document.body.appendChild(trigger);
    trigger.focus();
    const wrapper = mount(BasePopover, { props: { anchor: trigger }, attachTo: document.body });
    await wrapper.vm.$nextTick();
    expect(document.activeElement).toBe(document.querySelector('.base-popover'));
    wrapper.unmount();
    expect(document.activeElement).toBe(trigger);
    trigger.remove();
  });
});
