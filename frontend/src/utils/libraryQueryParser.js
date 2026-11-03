import { CancelLibraryQuery, GetTextAIStatus, ParseLibraryQuery, SearchParsedLibraryQuery } from '../../wailsjs/go/main/App';
import { experienceData } from './experienceResult.js';
export const libraryQueryAPI = {
  cancel: async requestID => experienceData(await CancelLibraryQuery(requestID)),
  status: async () => experienceData(await GetTextAIStatus()),
  parse: async request => experienceData(await ParseLibraryQuery(request)),
  search: async request => experienceData(await SearchParsedLibraryQuery(request))
};
export function queryResolution(draft, choices, removed) {
  return {
    draft_id: draft.draft_id,
    resolved_conditions: draft.conditions.filter(condition => !removed[condition.condition_id]).map(condition => ({ condition_id: condition.condition_id, entity_id: Number(choices[condition.condition_id]?.entity_id || 0), accept_person_relation: Boolean(choices[condition.condition_id]?.accept_person_relation) })),
    acknowledged_removals: [...draft.conditions.map(condition => condition.condition_id), ...draft.unsupported.map(issue => issue.id)].filter(id => removed[id])
  };
}
export function queryReady(draft, choices, removed) {
  if (!draft || draft.unsupported.some(issue => !removed[issue.id])) return false;
  const retained = draft.conditions.filter(condition => !removed[condition.condition_id]);
  return retained.length > 0 && retained.every(condition => {
    const choice = choices[condition.condition_id];
    return (!['person', 'tag'].includes(condition.kind) || Number(choice?.entity_id) > 0) && (!condition.requires_relation_acceptance || choice?.accept_person_relation);
  });
}
export function queryError(error) { return error?.code ? error.message : '调用未完成，请稍后重试'; }

export { newRequestID as newQueryRequestID } from './requestID.js';
