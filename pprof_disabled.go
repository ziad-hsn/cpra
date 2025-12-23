//go:build noprofile

package main

const defaultPprofEnabled = false

func setupPprof(enable bool, addr string) shutdowner {
	return nil
}

