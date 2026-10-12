// Package ocr reads the text off a photo of a page with the tesseract
// program. The photo goes in on stdin and the text comes out on stdout, so
// neither touches disk.
package ocr

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Tesseract reads English text off a photo by running the tesseract
// program found on the PATH.
type Tesseract struct{}

// ReadPhoto returns the text Tesseract reads off photo. If ctx ends first,
// the program is killed and ctx's error returned.
func (Tesseract) ReadPhoto(ctx context.Context, photo []byte) (string, error) {
	if pgm, ok := upright(photo); ok {
		photo = pgm
	}
	cmd := exec.CommandContext(ctx, "tesseract", "stdin", "stdout", "-l", "eng")
	cmd.Stdin = bytes.NewReader(photo)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// Tesseract's own messages say why (a missing language, an image
		// it can't decode); they never hold the text read.
		if msg := lastLine(stderr.String()); msg != "" {
			return "", fmt.Errorf("tesseract: %w: %s", err, msg)
		}
		return "", fmt.Errorf("tesseract: %w", err)
	}
	return out.String(), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndex(s, "\n")+1:]
}
