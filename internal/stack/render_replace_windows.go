package stack

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func replaceRenderedFile(from, to string) error {
	for attempt := 0; ; attempt++ {
		err := os.Rename(from, to) // #nosec G703 -- rendered paths are under the operator-owned space
		if err == nil || attempt == 19 || !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return err
		}
		// Docker bind readers and scanners can briefly omit FILE_SHARE_DELETE.
		time.Sleep(25 * time.Millisecond)
	}
}
