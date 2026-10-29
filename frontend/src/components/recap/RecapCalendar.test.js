import { mount } from '@vue/test-utils';
import { describe, it, expect } from 'vitest';
import RecapCalendar from './RecapCalendar.vue';

describe('calendar day selection', () => {
  for (const mode of ['month', 'year']) it(`${mode}: past and empty dates clickable, future and blanks disabled`, async () => {
    const wrapper = mount(RecapCalendar, { props: { mode, today: '2026-10-15', days: [{ date: '2026-10-01', plays: 2, views: 1 }, { date: '2026-10-15', plays: 0, views: 0 }, { date: '2026-10-16', plays: 0, views: 0 }] } });
    // Accessible labels use formatDateLabel's Chinese date.
    const first = wrapper.findAll('button').find(button => button.attributes('title')?.includes('播放 2 次'));
    await first.trigger('click');
    expect(wrapper.emitted('select-day')[0][0]).toMatchObject({ date: '2026-10-01', plays: 2, views: 1 });
    expect(wrapper.findAll('.is-future').every(button => button.element.disabled)).toBe(true);
    expect(wrapper.findAll('.is-blank').every(button => button.element.disabled)).toBe(true);
    const empty = wrapper.findAll('button:not(:disabled)').find(button => button.attributes('title')?.includes('播放 0 次'));
    await empty.trigger('click');
    expect(wrapper.emitted('select-day')).toHaveLength(2);
    wrapper.unmount();
  });
});
