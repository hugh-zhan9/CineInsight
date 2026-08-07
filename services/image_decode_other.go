//go:build !darwin

package services

import "context"

// sipsConvertJPEG 非 darwin 平台无 HEIC/RAW 解码能力，返回哨兵错误占位降级
// （设计 4.2.2 D-006，路由映射为 404）。
func sipsConvertJPEG(ctx context.Context, sourcePath, destinationPath string, maxEdge int) error {
	return ErrImageDecodeUnsupported
}

// sipsProbeDimensions 非 darwin 平台不探测 HEIC/RAW 尺寸（保持 0×0）。
func sipsProbeDimensions(ctx context.Context, sourcePath string) (int, int, error) {
	return 0, 0, ErrImageDecodeUnsupported
}
