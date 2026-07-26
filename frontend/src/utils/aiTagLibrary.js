export function groupAITagsByNamespace(tags) {
  const groups = [];
  const groupsByNamespace = new Map();

  for (const tag of Array.isArray(tags) ? tags : []) {
    const namespace = String(tag?.namespace || '').trim();
    let group = groupsByNamespace.get(namespace);
    if (!group) {
      group = { namespace, tags: [] };
      groupsByNamespace.set(namespace, group);
      groups.push(group);
    }
    group.tags.push({ ...tag });
  }

  return groups;
}

export function flattenAITagGroups(groups) {
  return (Array.isArray(groups) ? groups : []).flatMap(group => {
    const namespace = String(group?.namespace || '').trim();
    return (Array.isArray(group?.tags) ? group.tags : []).map(tag => ({
      id: Number(tag?.id) || 0,
      namespace,
      name: String(tag?.name || '').trim(),
      color: tag?.color || '',
      review_required: Boolean(tag?.review_required),
      is_active: Boolean(tag?.is_active)
    }));
  });
}

export function validateAITagGroups(groups) {
  const namespaces = new Set();
  const tagNames = new Set();

  for (const group of Array.isArray(groups) ? groups : []) {
    const namespace = String(group?.namespace || '').trim();
    if (!namespace) return '标签分类名称不能为空';
    const normalizedNamespace = namespace.toLocaleLowerCase();
    if (namespaces.has(normalizedNamespace)) return `标签分类重复：${namespace}`;
    namespaces.add(normalizedNamespace);

    const tags = Array.isArray(group?.tags) ? group.tags : [];
    if (tags.length === 0) return `分类“${namespace}”至少需要一个标签`;
    for (const tag of tags) {
      const name = String(tag?.name || '').trim();
      if (!name) return `分类“${namespace}”中存在空标签名称`;
      const normalizedName = name.toLocaleLowerCase();
      if (tagNames.has(normalizedName)) return `标签名称重复：${name}`;
      tagNames.add(normalizedName);
    }
  }

  return '';
}
