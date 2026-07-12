package minecraft

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/link-fgfgui/mod-downloader-core/networking"
)

type httpRangeReaderAt struct {
	client   *http.Client
	url      string
	size     int64
	deadline time.Time
}

func NewHTTPRangeReaderAt(url string) (*httpRangeReaderAt, error) {
	return newHTTPRangeReaderAt(url, 0)
}

func NewHTTPRangeReaderAtWithTimeout(url string, timeout time.Duration) (*httpRangeReaderAt, error) {
	return newHTTPRangeReaderAt(url, timeout)
}

func newHTTPRangeReaderAt(url string, timeout time.Duration) (*httpRangeReaderAt, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, fmt.Errorf("empty url")
	}

	clientTimeout := 20 * time.Second
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
		if timeout < clientTimeout {
			clientTimeout = timeout
		}
	}

	client := networking.NewClientWithUserAgent(clientTimeout, "mod-downloader/dev")
	resolvedURL, err := resolveHTTPDownloadURL(client, url, deadline)
	if err != nil {
		resolvedURL = url
	}

	size, err := probeHTTPRangeSize(client, resolvedURL, deadline)
	if err != nil && resolvedURL != url {
		size, err = probeHTTPRangeSize(client, url, deadline)
		resolvedURL = url
	}
	if err != nil {
		return nil, err
	}

	return &httpRangeReaderAt{
		client:   client,
		url:      resolvedURL,
		size:     size,
		deadline: deadline,
	}, nil
}

func resolveHTTPDownloadURL(client *http.Client, url string, deadline time.Time) (string, error) {
	req, cancel, err := newDeadlineRequest(http.MethodHead, url, deadline)
	if err != nil {
		return "", err
	}
	defer cancel()

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("resolve download url failed: status %d", resp.StatusCode)
	}
	return resp.Request.URL.String(), nil
}

func probeHTTPRangeSize(client *http.Client, url string, deadline time.Time) (int64, error) {
	req, cancel, err := newDeadlineRequest(http.MethodGet, url, deadline)
	if err != nil {
		return 0, err
	}
	defer cancel()
	req.Header.Set("Range", "bytes=0-0")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("range request unsupported: status %d", resp.StatusCode)
	}

	size, err := parseContentRangeSize(resp.Header.Get("Content-Range"))
	if err != nil {
		return 0, err
	}
	if size <= 0 {
		return 0, fmt.Errorf("invalid content size: %d", size)
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	return size, nil
}

func parseContentRangeSize(contentRange string) (int64, error) {
	_, sizeText, ok := strings.Cut(strings.TrimSpace(contentRange), "/")
	if !ok {
		return 0, fmt.Errorf("invalid content-range: %s", contentRange)
	}
	sizeText = strings.TrimSpace(sizeText)
	if sizeText == "" || sizeText == "*" {
		return 0, fmt.Errorf("unknown content size")
	}
	return strconv.ParseInt(sizeText, 10, 64)
}

func newDeadlineRequest(method string, url string, deadline time.Time) (*http.Request, context.CancelFunc, error) {
	ctx := context.Background()
	cancel := func() {}
	if !deadline.IsZero() {
		if time.Now().After(deadline) {
			return nil, cancel, context.DeadlineExceeded
		}
		ctx, cancel = context.WithDeadline(ctx, deadline)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	return req, cancel, nil
}

func (r *httpRangeReaderAt) Size() int64 {
	return r.size
}

func (r *httpRangeReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if !r.deadline.IsZero() && time.Now().After(r.deadline) {
		return 0, context.DeadlineExceeded
	}
	if off < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.size {
		return 0, io.EOF
	}

	want := len(p)
	end := off + int64(want) - 1
	if end >= r.size {
		end = r.size - 1
		want = int(end-off) + 1
	}

	req, cancel, err := newDeadlineRequest(http.MethodGet, r.url, r.deadline)
	if err != nil {
		return 0, err
	}
	defer cancel()
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end))

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		if off > 0 {
			if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
				return 0, err
			}
		}
		return readHTTPRangeBody(resp.Body, p, want)
	}
	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("range read failed: status %d", resp.StatusCode)
	}

	return readHTTPRangeBody(resp.Body, p, want)
}

func readHTTPRangeBody(body io.Reader, p []byte, want int) (int, error) {
	n, err := io.ReadFull(body, p[:want])
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	if n < len(p) && err == nil {
		err = io.EOF
	}
	return n, err
}
