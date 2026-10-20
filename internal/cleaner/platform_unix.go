//go:build !windows

package cleaner

import "os"

func checkPlatformAccess(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}
