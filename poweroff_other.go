//go:build !linux

package lifecycled

import "errors"

type localPoweroff struct{}

func newLocalPoweroff() localPoweroff {
	return localPoweroff{}
}

func (localPoweroff) PowerOff() error {
	return errors.New("power off is only supported on linux")
}
