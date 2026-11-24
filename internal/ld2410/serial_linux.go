//go:build linux

package ld2410

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Open opens the radar's UART raw at baud. 256000 is not a standard speed (stty refuses it), so it
// is set the way pyserial does: termios2 (TCSETS2) with BOTHER and the speed itself.
func Open(dev string, baud int) (*os.File, error) {
	f, err := os.OpenFile(dev, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS2)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", dev, err)
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | unix.CRTSCTS | unix.CBAUD
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | unix.BOTHER
	t.Ispeed, t.Ospeed = uint32(baud), uint32(baud)
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS2, t); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s at %d: %w", dev, baud, err)
	}
	return f, nil
}
