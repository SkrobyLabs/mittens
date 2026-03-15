//go:build windows

package main

func init() {
	platformStartBroker = func(a *App) {
		startBrokerTCP(a)
	}
}
