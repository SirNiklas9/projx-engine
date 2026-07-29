package main

func isUserHomeRoot(path string) bool {
	home, err := claudeHomeDir()
	if err != nil {
		return false
	}
	return pathEq(path, home)
}
