// 列表行的标签条带是单行横向溢出的（行高是虚拟列表的估算值，标签不能换行撑高），
// 所以"有多少标签被藏起来了"只能按几何算：右边缘超出条带可视宽度的就是看不见的那些。
// 留 1px 容差，免得亚像素宽度把刚好贴边的标签算成隐藏。
export function countHiddenTagBadges(stripWidth, badgeRightEdges) {
  const width = Number(stripWidth) || 0;
  if (width <= 0) return 0;
  const edges = Array.isArray(badgeRightEdges) ? badgeRightEdges : [];
  return edges.filter(edge => Number(edge) > width + 1).length;
}
