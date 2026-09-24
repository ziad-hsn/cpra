// Package clientconfig constructs SDK client settings from token files.
package clientconfig

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

// FromTokenFile constructs client settings that read a token file on each request. It never falls
// back to anonymous requests or logs a token/file payload after a read failure.
func FromTokenFile(server, tokenFile string, allowHTTP bool) (cpra.Config, error) {
	if tokenFile == "" {
		return cpra.Config{}, errors.New("a CPRa token file is required")
	}
	read := func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		file, err := os.Open(tokenFile)
		if err != nil {
			return "", errors.New("cannot open CPRa token file")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return "", errors.New("CPRa token file must be a regular file")
		}
		data, err := io.ReadAll(io.LimitReader(file, 16<<10+1))
		if err != nil || len(data) > 16<<10 {
			return "", errors.New("cannot read bounded CPRa token file")
		}
		token := strings.TrimSpace(string(data))
		if token == "" || strings.ContainsAny(token, "\r\n\x00") {
			return "", errors.New("CPRa token file contains an invalid token")
		}
		return token, ctx.Err()
	}
	if _, err := read(context.Background()); err != nil {
		return cpra.Config{}, err
	}
	return cpra.Config{BaseURL: server, TokenSource: read, AllowInsecureHTTP: allowHTTP}, nil
}
