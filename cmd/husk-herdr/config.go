package main

import "github.com/hyperlab-sh/husk/adapters/herdr/settings"

func checkConfig(path string) error {
	_, err := settings.Load(path)
	return err
}
