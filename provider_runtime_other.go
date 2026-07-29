//go:build !windows

package main

func preferProviderRuntime(provider, fallback string) string { return fallback }
