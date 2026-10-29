package vault

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// ErrTooLarge is returned when an inbound file exceeds the configured limit.
var ErrTooLarge = errors.New("vault: file exceeds the maximum size")

// Store is the global notes vault. Every path it produces is relative to a fixed
// root and is verified to stay inside it, so a model-supplied name can never
// escape the vault.
type Store struct {
	root     string
	maxBytes int64
	now      func() time.Time
}

// New builds a Store rooted at dir. The directory is created lazily on the first
// write, so a missing vault never blocks startup. maxBytes caps inbound files
// (0 means the Telegram cloud limit).
func New(dir string, maxBytes int64) *Store {
	if maxBytes <= 0 {
		maxBytes = 50 << 20 // 50 MB: the Bot API cloud upload ceiling
	}
	return &Store{root: dir, maxBytes: maxBytes, now: time.Now}
}

// Root returns the vault root as given.
func (s *Store) Root() string { return s.root }

// SaveInbound stores an inbound attachment under inbox/ and returns its path.
// The file name carries the date so the inbox stays ordered, and an existing
// name is never overwritten: a numeric suffix is added instead.
func (s *Store) SaveInbound(name string, r io.Reader) (string, error) {
	dir := filepath.Join(s.root, "inbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	clean := SanitizeName(name)
	target := filepath.Join(dir, s.now().Format("2006-01-02")+"-"+clean)
	target = uniquePath(target)

	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Read one byte past the limit so "exactly at the limit" is still accepted.
	written, err := io.Copy(f, io.LimitReader(r, s.maxBytes+1))
	if err != nil {
		os.Remove(target)
		return "", err
	}
	if written > s.maxBytes {
		os.Remove(target)
		return "", ErrTooLarge
	}
	return target, nil
}

// Note is one markdown note to be written into the vault. It mirrors the
// frontmatter convention already used by the vault's scripts: title,
// description, tags and date.
type Note struct {
	Folder      string
	Title       string
	Description string
	Body        string
	Tags        []string
}

// SaveNote writes a markdown note with the vault's frontmatter convention and
// returns its path. title becomes both the frontmatter title and the H1; the
// file name is the slugified title inside the given relative folder.
func (s *Store) SaveNote(n Note) (string, error) {
	dir, err := s.resolveDir(n.Folder)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(dir, Slug(n.Title)+".md")
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", yamlScalar(n.Title))
	if n.Description != "" {
		fmt.Fprintf(&b, "description: %s\n", yamlScalar(n.Description))
	}
	if n.Tags != nil {
		b.WriteString("tags:\n")
		for _, t := range n.Tags {
			fmt.Fprintf(&b, "  - %s\n", yamlScalar(t))
		}
	}
	fmt.Fprintf(&b, "date: %s\n", s.now().Format("2006-01-02"))
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", n.Title)
	b.WriteString(strings.TrimSpace(n.Body))
	b.WriteString("\n")

	return path, os.WriteFile(path, []byte(b.String()), 0o644)
}

// AppendLine appends a single line to an existing note, adding a newline only
// when the file does not already end with one. It refuses paths outside the
// vault.
func (s *Store) AppendLine(relPath, line string) error {
	path, err := s.resolve(relPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	prefix := ""
	if info.Size() > 0 {
		last := make([]byte, 1)
		if rf, err := os.Open(path); err == nil {
			if _, err := rf.ReadAt(last, info.Size()-1); err == nil && last[0] != '\n' {
				prefix = "\n"
			}
			rf.Close()
		}
	}
	_, err = f.WriteString(prefix + line + "\n")
	return err
}

// resolve joins a vault-relative path and guarantees the result stays inside the
// root, rejecting absolute paths, any ".." escape and any existing ancestor
// that reaches outside the root through a symlink.
func (s *Store) resolve(relPath string) (string, error) {
	clean := filepath.Clean(strings.TrimPrefix(relPath, string(filepath.Separator)))
	if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("vault: path %q escapes the vault", relPath)
	}
	full := filepath.Join(s.root, clean)
	if rel, err := filepath.Rel(s.root, full); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("vault: path %q escapes the vault", relPath)
	}
	// The store is what writes, so the symlink check lives here too: a directory
	// inside the vault that links outside must not become a write path, not even
	// when the leaf below it does not exist yet.
	if err := s.checkResolved(full); err != nil {
		return "", err
	}
	return full, nil
}

// checkResolved proves that path stays inside the root once symlinks are
// resolved. It resolves the nearest existing ancestor and re-appends the
// components that do not exist yet, because the unresolved form of a missing
// leaf cannot be proven contained.
func (s *Store) checkResolved(path string) error {
	root, err := filepath.Abs(s.root)
	if err != nil {
		return err
	}
	realRoot, err := resolvedForm(root)
	if err != nil {
		return fmt.Errorf("vault: path %q escapes the vault: %w", path, err)
	}
	resolved, err := resolvedForm(path)
	if err != nil {
		return fmt.Errorf("vault: path %q escapes the vault: %w", path, err)
	}
	if !within(realRoot, resolved) {
		return fmt.Errorf("vault: path %q escapes the vault", path)
	}
	return nil
}

// resolvedForm returns the symlink-resolved form of path. When parts of it do not
// exist yet, the nearest existing ancestor is resolved and the missing
// components are appended, so a missing leaf still carries the resolution of its
// parent instead of falling back to the unresolved path.
func resolvedForm(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	dir, base := filepath.Split(filepath.Clean(path))
	if base == "" || dir == "" || filepath.Clean(dir) == filepath.Clean(path) {
		return "", fmt.Errorf("no existing ancestor of %q", path)
	}
	parent, err := resolvedForm(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, base), nil
}

// within reports whether path is base or lives under it.
func within(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Store) resolveDir(folder string) (string, error) {
	if strings.TrimSpace(folder) == "" {
		return s.root, nil
	}
	return s.resolve(folder)
}

var (
	reNonSlug = regexp.MustCompile(`[^a-z0-9]+`)
	reUnsafe  = regexp.MustCompile(`[[:cntrl:]]`)
)

// SanitizeName reduces an attachment name to a safe basename: no directories, no
// control characters, no leading dots and a bounded length.
func SanitizeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = reUnsafe.ReplaceAllString(name, "")
	name = strings.TrimSpace(name)
	name = strings.Trim(name, ".")
	if name == "" {
		return "file"
	}
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

// Slug builds a lowercase, dash-separated identifier from a title, ignoring
// accents' case and dropping anything that is not alphanumeric.
func Slug(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	s := reNonSlug.ReplaceAllString(b.String(), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "nota"
	}
	if len(s) > 80 {
		s = strings.Trim(s[:80], "-")
	}
	return s
}

// uniquePath appends -1, -2, ... before the extension until the path is free.
func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// yamlScalar quotes a value when it contains characters that would break a plain
// YAML scalar.
func yamlScalar(s string) string {
	if strings.ContainsAny(s, ":#[]{}\"'&*!|>%@`\n") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
