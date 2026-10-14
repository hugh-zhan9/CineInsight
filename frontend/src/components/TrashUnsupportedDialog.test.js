import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';

import TrashUnsupportedDialog from './TrashUnsupportedDialog.vue';

const items = count => Array.from({ length: count }, (_, index) => ({ id: index + 1, name: `clip-${index + 1}.mp4` }));
const names = selector => Array.from(document.querySelectorAll(`[data-test="${selector}"] li`));
const click = selector => document.querySelector(`[data-test="${selector}"]`).click();

let wrapper;
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = '';
});

// 永久删除不可撤销：名单默认只列前几项，但确认前要能展开看到每一项。
describe('TrashUnsupportedDialog 名单', () => {
  it('选择页列前 10 项，「显示全部」展开看到每一项', async () => {
    wrapper = mount(TrashUnsupportedDialog, { props: { visible: true, items: items(45) }, attachTo: document.body });
    expect(names('trash-unsupported-names')).toHaveLength(11);
    expect(document.querySelector('[data-test="trash-unsupported-names"]').textContent).toContain('还有 35 项');
    click('trash-unsupported-show-all');
    await wrapper.vm.$nextTick();
    const shown = names('trash-unsupported-names');
    expect(shown).toHaveLength(45);
    expect(shown[44].textContent).toBe('clip-45.mp4');
    expect(document.querySelector('[data-test="trash-unsupported-show-all"]')).toBeNull();
  });

  it('确认页列前 30 项，同样能展开全部；重新打开回到只列前几项', async () => {
    wrapper = mount(TrashUnsupportedDialog, { props: { visible: true, items: items(45) }, attachTo: document.body });
    click('trash-unsupported-permanent');
    await wrapper.vm.$nextTick();
    expect(names('trash-unsupported-confirm-names')).toHaveLength(31);
    click('trash-unsupported-confirm-show-all');
    await wrapper.vm.$nextTick();
    expect(names('trash-unsupported-confirm-names')).toHaveLength(45);

    await wrapper.setProps({ visible: false });
    await wrapper.setProps({ visible: true });
    expect(names('trash-unsupported-names')).toHaveLength(11);
  });

  it('不超过上限时不给展开', () => {
    wrapper = mount(TrashUnsupportedDialog, { props: { visible: true, items: items(3) }, attachTo: document.body });
    expect(names('trash-unsupported-names')).toHaveLength(3);
    expect(document.querySelector('[data-test="trash-unsupported-show-all"]')).toBeNull();
  });
});
