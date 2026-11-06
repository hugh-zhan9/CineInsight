// One App-owned, memory-only exposure ring. Page batches and request lifetimes stay with each page.
export function createDiscoverySession() { return { recent: [], round: 0, epoch: 0 }; }
export function resetDiscoverySession(session) { session.recent = []; session.round = 0; session.epoch += 1; }
export function recordDiscoveryExposure(session, id) {
  id = Number(id); if (!Number.isSafeInteger(id) || id <= 0) return;
  session.recent = [...session.recent.filter(value => value !== id), id].slice(-100);
}
export function discoveryExclusions(session, hardIDs) {
  const hard = [...new Set(hardIDs.map(Number).filter(id => Number.isSafeInteger(id) && id > 0))];
  if (hard.length > 100) throw new Error('当前推荐排除项超出上限');
  const room = 100 - hard.length;
  return [...(room ? session.recent.filter(id => !hard.includes(id)).slice(-room) : []), ...hard];
}
export async function fetchDiscoveryBatch(fetch, session, request, limit, hardIDs = [], isCurrent = () => true) {
  const epoch = session.epoch;
  const valid = () => session.epoch === epoch && isCurrent();
  const hard = [...new Set(hardIDs.map(Number))];
  const clean = rows => {
    const seen = new Set(hard);
    return (rows || []).filter(row => {
      const id = Number(row?.video?.id);
      if (!Number.isSafeInteger(id) || id <= 0 || seen.has(id)) return false;
      seen.add(id); return true;
    }).slice(0, limit);
  };
  let items = clean(await fetch({ ...request, exclude_ids: discoveryExclusions(session, hard) }, limit));
  if (!valid()) return null;
  let newRound = false;
  if (!items.length && session.recent.some(id => !hard.includes(id))) {
    // Only presentation history is soft. User filters and current cards survive the new round.
    items = clean(await fetch({ ...request, exclude_ids: hard }, limit));
    if (!valid()) return null;
    session.recent = []; session.round += 1; newRound = true;
  }
  return { items, newRound };
}
export function observeDiscoveryExposure(root, session, active) {
  const nodes = [...(root?.querySelectorAll('[data-discovery-id]') || [])];
  if (typeof IntersectionObserver !== 'function') {
    if (active()) nodes.forEach(node => recordDiscoveryExposure(session, node.dataset.discoveryId));
    return null;
  }
  const observer = new IntersectionObserver(entries => {
    if (!active()) return;
    entries.filter(entry => entry.isIntersecting).forEach(entry => recordDiscoveryExposure(session, entry.target.dataset.discoveryId));
  });
  nodes.forEach(node => observer.observe(node));
  return observer;
}
