// 标签底色：把标签的 #rrggbb 主色换成 35% 透明度的底色，列表行与「今晚看什么」等处的标签共用。
// 非 #rrggbb 的值（空、CSS 变量、颜色名）原样返回。
export function tagBackgroundColor(hex) {
  if (!hex || !hex.startsWith('#')) return hex;
  const r = parseInt(hex.slice(1, 3), 16);
  const g = parseInt(hex.slice(3, 5), 16);
  const b = parseInt(hex.slice(5, 7), 16);
  return `rgba(${r},${g},${b},0.35)`;
}
