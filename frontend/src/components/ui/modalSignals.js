// 对话框打开的信号（BaseModal 入栈时在 window 上发出）。菜单与浮层排在对话框之上（--z-menu 高于 --z-modal，
// 因为它们可能从抽屉或对话框里点开），所以对话框一打开，开着的浮层要自己收起，不能盖住确认框、拦下给它的点击。
export const MODAL_OPEN_EVENT = 'cineinsight:modal-open';

// Esc 只关最上面一层（D-UD-13），全站一条规则：
// - 模态层按打开先后入栈，最后一个在最上面，Esc 只交给它；它接手时 preventDefault 并 stopPropagation，
//   window 上的页面级 Esc（列表取消选择等）收不到。栈里是 BaseModal（DialogShell、应用内确认框、命令面板都建在它上面），
//   以及不走 BaseModal 的全窗口层（版本对比播放器、人脸来源详情）：它们在 window 捕获阶段接 Esc，
//   只在自己是栈顶时接手，上面叠着对话框时让给它。
// - 非模态层（任务中心这类抽屉）只要有模态层开着就不接 Esc：模态层总在抽屉上面（--z-modal 高于 --z-drawer），
//   抽屉又常挂载、比后开的对话框先在 document 上登记 keydown，不让就会一下 Esc 关两层。
// - 层里的小浮层（BasePopover、输入框的候选下拉）在自己的节点上接 Esc 并 stopPropagation，只收起自己。
// 入栈要在子组件挂载之前（beforeMount）：父组件的 beforeMount 早于子组件，内容里同时出现的子对话框排在它上面。
const modalLayers = [];

export function openModalLayer(owner) {
  modalLayers.push(owner);
}

export function closeModalLayer(owner) {
  const index = modalLayers.indexOf(owner);
  if (index !== -1) modalLayers.splice(index, 1);
}

export function isTopModalLayer(owner) {
  return modalLayers.length > 0 && modalLayers[modalLayers.length - 1] === owner;
}

export function hasOpenModalLayer() {
  return modalLayers.length > 0;
}
