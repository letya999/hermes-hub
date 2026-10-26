package hosting

import (
	"golang.org/x/sys/windows"
	"os"
)

func readFlags() int                   { return os.O_RDONLY }
func multipleLinks(i os.FileInfo) bool { return false } // Windows handle link-count is checked after open.
func syncDirectory(string) error       { return nil }   // Windows rename is atomic; native power-loss acceptance requires the deployment Linux filesystem.
func openedMultipleLinks(f *os.File) bool {
	var info windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info) != nil || info.NumberOfLinks != 1
}
