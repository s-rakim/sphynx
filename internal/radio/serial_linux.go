//go:build linux

package radio

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openSerial opens a radio on a USB serial port: 115200 baud, 8N1, raw.
func openSerial(path string) (stream, error) {
	f, err := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	rc, err := f.SyscallConn()
	if err != nil {
		f.Close()
		return nil, err
	}
	var serr error
	err = rc.Control(func(fd uintptr) {
		t, err := unix.IoctlGetTermios(int(fd), unix.TCGETS)
		if err != nil {
			serr = err
			return
		}
		t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
		t.Oflag &^= unix.OPOST
		t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
		t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | unix.CRTSCTS | unix.CBAUD
		t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | unix.B115200
		t.Ispeed, t.Ospeed = unix.B115200, unix.B115200
		t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
		serr = unix.IoctlSetTermios(int(fd), unix.TCSETS, t)
	})
	if err == nil {
		err = serr
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s isn't a serial port Sphynx can use: %w", path, err)
	}
	return f, nil
}
