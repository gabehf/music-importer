package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Hard-coded for now; move to config/env later.
var (
	gotifyURL      = os.Getenv("GOTIFY_URL")
	gotifyToken    = os.Getenv("GOTIFY_TOKEN")
	gotifyPriority = 5
)

var gotifyClient = &http.Client{Timeout: 10 * time.Second}

type gotifyMessage struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
}

// SendGotify sends a push notification with the given title and message to
// the configured Gotify server.
func SendGotify(title, message string) error {

	if gotifyURL == "" || gotifyToken == "" {
		return errors.New("GOTIFY_URL and GOTIFY_TOKEN must be set to use Gotify notifications")
	}

	body, err := json.Marshal(gotifyMessage{
		Title:    title,
		Message:  message,
		Priority: gotifyPriority,
	})
	if err != nil {
		return fmt.Errorf("gotify: marshal: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(gotifyURL, "/")+"/message", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("gotify: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gotify-Key", gotifyToken)

	resp, err := gotifyClient.Do(req)
	if err != nil {
		return fmt.Errorf("gotify: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("gotify: unexpected status %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}
