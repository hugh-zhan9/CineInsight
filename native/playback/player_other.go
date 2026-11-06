//go:build !darwin || !cgo

package playback

import (
	"errors"
	"os"
)

var errUnavailable = errors.New("native playback unavailable on this platform")

type Player struct{}

func New(string) (*Player, error)                                       { return nil, errUnavailable }
func (*Player) Load(string) error                                       { return errUnavailable }
func (*Player) LoadFile(*os.File) error                                 { return errUnavailable }
func (*Player) Command(string, float64) error                           { return errUnavailable }
func (*Player) Viewport(float64, float64, float64, float64, bool) error { return errUnavailable }
func (*Player) Snapshot() (Snapshot, error)                             { return Snapshot{}, errUnavailable }
func (*Player) Subtitles(any) error                                     { return errUnavailable }
func (*Player) Close()                                                  {}
func RegisterWindow() error                                             { return errUnavailable }
func UnregisterWindow()                                                 {}
func WindowEpoch() uint64                                               { return 0 }

func (*Player) LoadFileAt(*os.File, int64) error { return errUnavailable }
