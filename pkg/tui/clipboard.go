//go:build !windows

package tui

import (
	"encoding/base64"
	"fmt"
	"os"
)

// copyTextToClipboard writes text to the terminal clipboard via OSC 52. The
// sequence is in-band, so it works over SSH and inside tmux (with
// set-clipboard on) where xclip-style helpers cannot reach the local
// clipboard. It draws nothing, so writing it mid-frame is harmless.
func copyTextToClipboard(text string) error {
	_, err := fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(text)))
	return err
}
