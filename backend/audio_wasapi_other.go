//go:build !windows

package main

import "errors"

func newWASAPICapture(source string) (audioCapture, error) {
	return nil, errors.New("WASAPI 音频采集仅支持 Windows")
}
