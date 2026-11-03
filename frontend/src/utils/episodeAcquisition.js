import { DescribeEpisodeCandidates, PrepareEpisodeResourceSearch, SubmitEpisodeDownload, ListEpisodeAcquisitions, PreviewEpisodeAssociation, ConfirmEpisodeAssociation } from '../../wailsjs/go/main/App';
import { experienceData } from './experienceResult.js';
export const episodeAPI = {
  candidates: async request => experienceData(await DescribeEpisodeCandidates(request)),
  prepare: async request => experienceData(await PrepareEpisodeResourceSearch(request)),
  submit: async request => experienceData(await SubmitEpisodeDownload(request)),
  list: async request => experienceData(await ListEpisodeAcquisitions(request)),
  preview: async request => experienceData(await PreviewEpisodeAssociation(request)),
  confirm: async token => experienceData(await ConfirmEpisodeAssociation(token))
};
export function episodeError(error) { return error?.code ? error.message : '操作未完成，请刷新后重试'; }
export function episodeNumbers(text) {
  const pieces = String(text || '').trim().split(/[,，、\s]+/).filter(Boolean);
  const result = [], seen = new Set();
  if (!pieces.length) throw new Error('请输入本次要找的集号');
  for (const piece of pieces) {
    const match = /^(\d+)(?:[-–~至](\d+))?$/.exec(piece);
    if (!match) throw new Error('集号可写成 1、3、5-8');
    const low = Number(match[1]), high = Number(match[2] || match[1]);
    if (low < 1 || high > 1000000 || high < low || result.length + high - low + 1 > 200) throw new Error('每次请选择 1 至 200 集，范围需有效');
    for (let value = low; value <= high; value++) { if (seen.has(value)) throw new Error('集号不能重复'); seen.add(value); result.push(value); }
  }
  return result.sort((a, b) => a - b);
}
export function episodeNumber(value, maximum, minimum = 0) {
  if (value === '' || value === null || value === undefined) return null;
  const number = Number(value); return Number.isInteger(number) && number >= minimum && number <= maximum ? number : null;
}
export const EPISODE_SOURCE_STATES = { selected: '已提交选择', transferring: '处理中', imported_unlinked: '已入库，待核对', linked: '已关联', import_failed: '入库未完成', detached: '原作品集已删除' };
