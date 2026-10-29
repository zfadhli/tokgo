package cli

import "fmt"

// banner prints the wordmark and version above the run, or above the usage text
// when the command line is rejected.
func banner(version string) {
	fmt.Printf("%sv%s\n\n", bannerArt, version)
}

const bannerArt = `
      ___           ___           ___           ___           ___
     /\  \         /\  \         /\__\         /\  \         /\  \
     \:\  \       /::\  \       /:/  /        /::\  \       /::\  \
      \:\  \     /:/\:\  \     /:/__/        /:/\:\  \     /:/\:\  \
      /::\  \   /:/  \:\  \   /::\__\____   /:/  \:\  \   /:/  \:\  \
     /:/\:\__\ /:/__/ \:\__\ /:/\:::::\__\ /:/__/_\:\__\ /:/__/ \:\__\
    /:/  \/__/ \:\  \ /:/  / \/_|:|~~|~    \:\  /\ \/__/ \:\  \ /:/  /
   /:/  /       \:\  /:/  /     |:|  |      \:\ \:\__\    \:\  /:/  /
   \/__/         \:\/:/  /      |:|  |       \:\/:/  /     \:\/:/  /
                  \::/  /       |:|  |        \::/  /       \::/  /
                   \/__/         \|__|         \/__/         \/__/
`
