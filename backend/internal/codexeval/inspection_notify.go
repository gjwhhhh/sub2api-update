package codexeval

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func SendInspectionNotification(ctx context.Context, s InspectionSettings, title, message string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	send := func(channel, endpoint, contentType, body string, auth bool) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
		if err != nil {
			return fmt.Errorf("%s: invalid notification address", channel)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if auth && s.NtfyPassword != "" {
			req.SetBasicAuth(s.NtfyUsername, s.NtfyPassword)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("%s: notification request failed", channel)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("%s: HTTP %d", channel, resp.StatusCode)
		}
		return nil
	}
	var failures []error
	if s.NtfyServer != "" && s.NtfyTopic != "" {
		endpoint := strings.TrimRight(s.NtfyServer, "/") + "/" + url.PathEscape(s.NtfyTopic) + "?" + url.Values{"title": {title}}.Encode()
		if err := send("ntfy", endpoint, "text/plain; charset=utf-8", message, true); err != nil {
			failures = append(failures, err)
		}
	}
	if s.BarkURL != "" {
		body := url.Values{"title": {title}, "body": {message}}.Encode()
		if err := send("Bark", s.BarkURL, "application/x-www-form-urlencoded", body, false); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
