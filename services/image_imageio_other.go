//go:build !darwin

package services

import (
	"context"
	"errors"
)

var errNativeImageIOUnavailable = errors.New("native imageio unavailable")

func nativeFastImageThumbnail(_ context.Context, _, _ string, _ int) error {
	return errNativeImageIOUnavailable
}

func nativeFastImageProbe(_ context.Context, _ string) (int, int, error) {
	return 0, 0, errNativeImageIOUnavailable
}
