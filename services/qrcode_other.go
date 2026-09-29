//go:build !darwin || !cgo

package services

// 非 darwin 平台、以及没有 cgo 的构建没有二维码生成器（D-PC47）：CoreImage 是 macOS 的组件，
// 这里既没有等价物也不打算引入第三方库。返回空串表示「不支持」，前端据此不显示二维码。

func generateQRCodePNG(text string) ([]byte, error) {
	return nil, nil
}
