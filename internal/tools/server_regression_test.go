package tools

// Regression probes derived from T7's contract in odd/tasks/telegram-bridge.md.
// S7: every vault path is resolved against the root and rejected when it escapes.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// hashTree fingerprints every entry under root so a rejected call is provably
// side-effect free.
func hashTree(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		sum := ""
		if info.Mode().IsRegular() {
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			h := sha256.Sum256(data)
			sum = hex.EncodeToString(h[:8])
		}
		rel, _ := filepath.Rel(root, p)
		lines = append(lines, fmt.Sprintf("%s|%s|%d|%s", rel, info.Mode(), info.Size(), sum))
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(lines)
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(h[:16])
}

// postWithHeader sends one request, always setting the token header, so an
// empty token can be distinguished from an absent one.
func postWithHeader(t *testing.T, url, token string, payload any) (int, string) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, strings.TrimSpace(buf.String())
}

// TestSaveNoteRejectsAFolderThatEscapesThroughASymlinkedDirectory is S7: a
// symlinked directory inside the vault must not become a write path to the
// outside, not even when the leaf below it does not exist yet.
func TestSaveNoteRejectsAFolderThatEscapesThroughASymlinkedDirectory(t *testing.T) {
	h := newHarness(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(h.dir, "enlace")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before := hashTree(t, h.dir)

	res := h.post(t, saveNotePath, map[string]any{"title": "Escapa", "body": "cuerpo", "folder": "enlace/sub"})
	if res.status == http.StatusOK {
		t.Errorf("status = 200 for a folder reaching outside the vault (body %q)", res.raw)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("save_note wrote %v outside the vault root %s", entries, h.dir)
	}
	if after := hashTree(t, h.dir); after != before {
		t.Errorf("vault changed after a rejected save")
	}
}

// TestSendFileRejectsASymlinkedDirectory is S7/S4: the sender is never reached
// for a path that leaves the vault through an internal symlink.
func TestSendFileRejectsASymlinkedDirectory(t *testing.T) {
	h := newHarness(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secreto.txt"), []byte("secreto"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(h.dir, "dir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, p := range []string{"dir/secreto.txt", "dir/../dir/secreto.txt"} {
		res := h.post(t, sendFilePath, map[string]any{"path": p})
		res.errorText(t)
		if len(h.files.calls) != 0 {
			t.Errorf("path %q reached the sender: %v", p, h.files.calls)
		}
	}
}

// TestTokenVariantsReachNoHandler is the authorization rule: absent, empty,
// same-length wrong, truncated and extended tokens are all 403 with no side
// effect, and authorization runs before routing.
func TestTokenVariantsReachNoHandler(t *testing.T) {
	h := newHarness(t)
	writeVaultFile(t, h.dir, filepath.Join("alter", "ideas.md"), "# Ideas\n")
	writeVaultFile(t, h.dir, filepath.Join("alter", "informe.pdf"), "%PDF")
	before := hashTree(t, h.dir)
	good := h.server.Token()

	for _, tok := range []string{"", strings.Repeat("a", len(good)), good[:len(good)-1], good + "a", strings.ToUpper(good)} {
		for _, endpoint := range []string{saveNotePath, appendNotePath, sendFilePath, askOptionsPath} {
			status, raw := postWithHeader(t, h.base+endpoint, tok, map[string]any{
				"title": "Token", "body": "x", "note": "alter/ideas.md", "line": "- x",
				"path": "alter/informe.pdf", "question": "¿Sigo?", "options": []string{"sí", "no"},
			})
			if status != http.StatusForbidden {
				t.Errorf("%s with token %q: status = %d, want 403 (body %s)", endpoint, tok, status, raw)
			}
		}
	}
	if len(h.files.calls) != 0 || h.asker.calls != 0 {
		t.Errorf("rejected tokens reached sender=%d asker=%d", len(h.files.calls), h.asker.calls)
	}
	if after := hashTree(t, h.dir); after != before {
		t.Errorf("vault changed under rejected tokens")
	}

	// Authorization must precede routing: an unknown route answers 403 without a
	// token and 404 with one.
	if status, raw := postWithHeader(t, h.base+"/tools/unknown", "", map[string]any{}); status != http.StatusForbidden {
		t.Errorf("unauthenticated /tools/unknown: status = %d, want 403 (body %s)", status, raw)
	}
	if status, _ := postWithHeader(t, h.base+"/tools/unknown", good, map[string]any{}); status != http.StatusNotFound {
		t.Errorf("authenticated /tools/unknown: status = %d, want 404", status)
	}
}

// countAsker tracks concurrent entries to prove the server does not serialize.
type countAsker struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	entered  chan struct{}
	release  chan struct{}
}

func (c *countAsker) AskOptions(ctx context.Context, question string, options []string) (string, error) {
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.peak {
		c.peak = c.inFlight
	}
	c.mu.Unlock()
	select {
	case c.entered <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
	case <-ctx.Done():
		c.mu.Lock()
		c.inFlight--
		c.mu.Unlock()
		return "", ctx.Err()
	}
	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	return options[0], nil
}

// TestAskOptionsDoesNotSerializeRequests is S7/contract: three concurrent asks
// all reach the asker instead of queueing behind each other.
func TestAskOptionsDoesNotSerializeRequests(t *testing.T) {
	asker := &countAsker{entered: make(chan struct{}, 8), release: make(chan struct{})}
	h := newHarnessWith(t, func(cfg *Config) { cfg.Asker = asker; cfg.AskTimeout = 30 * time.Second })

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := h.post(t, askOptionsPath, map[string]any{"question": fmt.Sprintf("q%d", i), "options": []string{"a", "b"}})
			if res.status != http.StatusOK {
				t.Errorf("ask %d: status = %d (body %s)", i, res.status, res.raw)
			}
		}(i)
	}
	timeout := time.After(5 * time.Second)
	seen := 0
	for seen < 3 {
		select {
		case <-asker.entered:
			seen++
		case <-timeout:
			t.Fatalf("only %d of 3 asks reached the asker: the server serializes them", seen)
		}
	}
	close(asker.release)
	wg.Wait()
	asker.mu.Lock()
	defer asker.mu.Unlock()
	if asker.peak != 3 {
		t.Errorf("peak concurrent asker calls = %d, want 3", asker.peak)
	}
}

// TestDefaultAskTimeoutIsTenMinutes pins the documented bound: an unset
// Config.AskTimeout must resolve to 10 minutes, not to an arbitrary default.
func TestDefaultAskTimeoutIsTenMinutes(t *testing.T) {
	h := newHarnessWith(t, func(cfg *Config) { cfg.AskTimeout = 0 })
	if got := h.server.cfg.AskTimeout; got != 10*time.Minute {
		t.Errorf("AskTimeout = %s, want 10m", got)
	}
}

// TestSendFileOnADirectoryIsABusinessError keeps the contract's 4xx rule: a
// directory is a model mistake, not a server failure.
func TestSendFileOnADirectoryIsABusinessError(t *testing.T) {
	h := newHarness(t)
	writeVaultFile(t, h.dir, filepath.Join("alter", "ideas.md"), "# Ideas\n")
	res := h.post(t, sendFilePath, map[string]any{"path": "alter"})
	if res.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a directory (body %s)", res.status, res.raw)
	}
}
