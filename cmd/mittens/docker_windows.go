//go:build windows

package main

func init() {
	platformCurrentUserIDs = func() (int, int) {
		return 1000, 1000
	}

	platformPreBuildHook = func(dockerfile string) {
		ensureBaseImages(dockerfile)
	}
}
