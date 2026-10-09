package gguf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// InitialRange is the first Range FetchHeader asks for; MaxRange is the most
// it will ask for before giving up. A header with a big vocabulary is a few
// MB, so one or two requests are usual.
var (
	InitialRange int64 = 2 << 20
	MaxRange     int64 = 256 << 20
)

// FetchHeader reads a model's facts over HTTP Range requests, growing the
// range until the tensor infos parse, so they are known before a byte of the
// weights is downloaded. A nil client uses http.DefaultClient.
func FetchHeader(ctx context.Context, client *http.Client, url string) (Facts, error) {
	if client == nil {
		client = http.DefaultClient
	}
	size := InitialRange
	for {
		data, total, err := rangeGet(ctx, client, url, size)
		if err != nil {
			return Facts{}, err
		}
		h, rerr := Read(bytes.NewReader(data), true)
		if rerr == nil {
			f, err := Derive(h)
			if total > f.TotalBytes {
				f.TotalBytes = total
			}
			return f, err
		}
		short := errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF)
		if !short {
			return Facts{}, rerr
		}
		if int64(len(data)) < size || (total > 0 && size >= total) {
			return Facts{}, fmt.Errorf("gguf: header ends before the tensor index: %w", rerr)
		}
		if size >= MaxRange {
			return Facts{}, fmt.Errorf("gguf: header larger than %d bytes", MaxRange)
		}
		size *= 4
		if size > MaxRange {
			size = MaxRange
		}
	}
}

// rangeGet returns the first n bytes of url and the file's total size when
// the server says it.
func rangeGet(ctx context.Context, c *http.Client, url string, n int64) ([]byte, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", n-1))
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	var total int64
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndex(cr, "/"); i >= 0 {
			total, _ = strconv.ParseInt(cr[i+1:], 10, 64)
		}
	} else if resp.StatusCode == http.StatusOK {
		total = resp.ContentLength
	}
	// A server that ignores Range sends the whole file: take only what we asked for.
	data, err := io.ReadAll(io.LimitReader(resp.Body, n))
	return data, total, err
}
