package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

//go:embed chat-system.md
var defaultChatPrompt string

type chatSettings struct {
	BaseURL     string  `json:"base_url"`
	APIKey      string  `json:"api_key,omitempty"`
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
}

func chatID() string             { return hex.EncodeToString(randBytes()) }
func randBytes() []byte          { b := make([]byte, 16); _, _ = rand.Read(b); return b }
func validChatID(id string) bool { _, err := hex.DecodeString(id); return len(id) == 32 && err == nil }

func (a *chatApp) readFile(name string) ([]byte, error) {
	root, err := os.OpenRoot(a.store.Directory())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return fs.ReadFile(root.FS(), name)
}

// Publication retries the same bytes. A failed flush never becomes an acknowledged
// save, and an interrupted tool-result write is recovered through its MCP receipt.
func (a *chatApp) publish(ctx context.Context, name string, data []byte) error {
	for {
		err := a.publishOnce(name, data)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (a *chatApp) publishOnce(name string, data []byte) error {
	root, err := os.OpenRoot(a.store.Directory())
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", name)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if current, err := fs.ReadFile(root.FS(), name); err != nil || !bytes.Equal(current, data) {
		sum := sha256.Sum256(data)
		tmp := name + "." + hex.EncodeToString(sum[:]) + ".tmp"
		if info, err := root.Lstat(tmp); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", tmp)
		}
		file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		_, written := file.Write(data)
		if err := errors.Join(written, file.Sync(), file.Close()); err != nil {
			return err
		}
		if err := root.Rename(tmp, name); err != nil {
			return err
		}
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	dir, err := root.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func (a *chatApp) fileLock(ctx context.Context, name string, wait bool) (*flock.Flock, error) {
	root, err := os.OpenRoot(a.store.Directory())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular lock file", name)
	}
	lock := flock.New(filepath.Join(a.store.Directory(), name), flock.SetPermissions(0600))
	var ok bool
	if wait {
		ok, err = lock.TryLockContext(ctx, 25*time.Millisecond)
	} else {
		ok, err = lock.TryLock()
	}
	if err != nil || !ok {
		_ = lock.Close()
		if err == nil {
			err = fmt.Errorf("conversation is already active")
		}
		return nil, err
	}
	return lock, nil
}

func (a *chatApp) settings() (chatSettings, error) {
	settings := chatSettings{Temperature: 0.15}
	data, err := a.readFile("chat-settings.json")
	if errors.Is(err, fs.ErrNotExist) {
		return settings, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &settings)
	}
	return settings, err
}

func (s chatSettings) client() openai.Client {
	return openai.NewClient(option.WithBaseURL(s.BaseURL), option.WithAPIKey(s.APIKey), option.WithOrganization(""), option.WithProject(""), option.WithMaxRetries(2), option.WithRequestTimeout(0))
}

func (a *chatApp) settingsHTTP(w http.ResponseWriter, r *http.Request) {
	if err := a.initialize(r.Context()); err != nil {
		fail(w, err)
		return
	}
	if r.Method == http.MethodPut {
		var input struct {
			BaseURL     string          `json:"base_url"`
			Model       string          `json:"model"`
			APIKey      *string         `json:"api_key"`
			Temperature json.RawMessage `json:"temperature"`
		}
		if !decode(w, r, &input) {
			return
		}
		base := strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
		base = strings.TrimSuffix(base, "/chat/completions")
		u, err := url.Parse(base)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			writeStatus(w, 400, map[string]any{"error": "Enter an HTTP or HTTPS API base URL, such as http://localhost:8080/v1."})
			return
		}
		lock, err := a.fileLock(r.Context(), ".chat-settings.lock", true)
		if err != nil {
			fail(w, err)
			return
		}
		defer lock.Close()
		s, err := a.settings()
		if err != nil {
			fail(w, err)
			return
		}
		s.BaseURL, s.Model = base+"/", strings.TrimSpace(input.Model)
		if input.Temperature != nil {
			text := strings.TrimSpace(string(input.Temperature))
			if strings.HasPrefix(text, "\"") {
				if err := json.Unmarshal(input.Temperature, &text); err != nil {
					writeStatus(w, 400, map[string]any{"error": "Temperature must be a number from 0 to 2. Use 0.15 for memory work."})
					return
				}
			}
			value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
			if err != nil || !(value >= 0 && value <= 2) {
				writeStatus(w, 400, map[string]any{"error": "Temperature must be a number from 0 to 2. Use 0.15 for memory work."})
				return
			}
			s.Temperature = value
		}
		if input.APIKey != nil {
			s.APIKey = strings.TrimSpace(*input.APIKey)
		}
		data, err := json.MarshalIndent(s, "", "  ")
		if err == nil {
			err = a.publish(r.Context(), "chat-settings.json", data)
		}
		if err != nil {
			fail(w, err)
			return
		}
	}
	s, err := a.settings()
	if err != nil {
		fail(w, err)
		return
	}
	write(w, map[string]any{"base_url": s.BaseURL, "model": s.Model, "temperature": s.Temperature, "has_api_key": s.APIKey != "", "configured": s.BaseURL != "" && s.Model != "", "prompt_file": "chat-system.md"})
}

func (a *chatApp) modelsHTTP(w http.ResponseWriter, r *http.Request) {
	s, err := a.settings()
	if err != nil {
		fail(w, err)
		return
	}
	if s.BaseURL == "" {
		writeStatus(w, 400, map[string]any{"error": "Save the API base URL before loading models."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	client := s.client()
	page, err := client.Models.List(ctx)
	if err != nil {
		writeStatus(w, 502, map[string]any{"error": safeChatError(err, s.APIKey)})
		return
	}
	names := []string{}
	for _, model := range page.Data {
		names = append(names, model.ID)
	}
	write(w, map[string]any{"models": names})
}

func safeChatError(err error, key string) string {
	message := err.Error()
	if key != "" {
		message = strings.ReplaceAll(message, key, "[REDACTED]")
	}
	return message
}
