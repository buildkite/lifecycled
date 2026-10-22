//go:build linux

package lifecycled

import "golang.org/x/sys/unix"

type localPoweroff struct{}

func newLocalPoweroff() localPoweroff {
	return localPoweroff{}
}

func (localPoweroff) PowerOff() error {
	return unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}
