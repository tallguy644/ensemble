//go:build !windows

package main

import "errors"

func CanPickFolder() bool { return false }

func PickFolder() (string, error) {
	return "", errors.New("The folder picker is only available on Windows; type the folder path instead.")
}
