//go:build !linux

package radio

import "errors"

func openSerial(path string) (stream, error) {
	return nil, errors.New("radios on USB work when Sphynx runs on Linux; connect the radio over Wi-Fi or Ethernet instead")
}
