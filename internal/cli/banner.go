package cli

import (
	"fmt"

	"tokgo/internal/ui"
)

// banner prints the wordmark and version above the run, or above the usage text
// when the command line is rejected.
func banner(version string) {
	fmt.Print(bannerArt)
	fmt.Println(ui.Bold("tokgo v"+version) + " — TikTok LIVE recorder")
	fmt.Println(ui.Dim("https://github.com/zfadhli/tokgo · MIT License"))
	fmt.Println()
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
