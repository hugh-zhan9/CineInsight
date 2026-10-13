// 对话框打开的信号（BaseModal 入栈时在 window 上发出）。菜单与浮层排在对话框之上（--z-menu 高于 --z-modal，
// 因为它们可能从抽屉或对话框里点开），所以对话框一打开，开着的浮层要自己收起，不能盖住确认框、拦下给它的点击。
export const MODAL_OPEN_EVENT = 'cineinsight:modal-open';
