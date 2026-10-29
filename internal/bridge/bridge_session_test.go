package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const vScen = "BRIDGE_VERIFY_SCENARIO"

// TestVerifyFakePiProcess is the fake `pi --mode rpc` child (scenarios: echo,
// hang, crash, handled, null_text).
func TestVerifyFakePiProcess(t *testing.T) {
	if os.Getenv(vScen) == "" {
		return
	}
	in := bufio.NewScanner(os.Stdin)
	emit := func(s string) { _, _ = io.WriteString(os.Stdout, s+"\n") }
	var last string
	for in.Scan() {
		var cmd struct{ Type, ID, Message string }
		if json.Unmarshal(in.Bytes(), &cmd) != nil {
			continue
		}
		sc := os.Getenv(vScen)
		switch cmd.Type {
		case "prompt":
			last = cmd.Message
			switch sc {
			case "handled":
				emit(`{"type":"response","command":"prompt","success":true,"data":{"disposition":"handled"}}`)
			case "hang":
				emit(`{"type":"response","command":"prompt","success":true,"data":{"disposition":"started"}}`)
				time.Sleep(time.Hour)
			case "crash":
				emit(`{"type":"response","command":"prompt","success":true,"data":{"disposition":"started"}}`)
				return
			default:
				emit(`{"type":"response","command":"prompt","success":true,"data":{"disposition":"started"}}`)
				emit(`{"type":"agent_settled"}`)
			}
		case "get_last_assistant_text":
			data := `{"text":null}`
			if os.Getenv(vScen) != "null_text" {
				b, _ := json.Marshal(last)
				data = `{"text":` + string(b) + `}`
			}
			emit(`{"type":"response","command":"get_last_assistant_text","success":true,"data":` + data + `}`)
		}
	}
	os.Exit(0)
}

type vSpawnLog struct {
	mu   sync.Mutex
	args [][]string
}

func (v *vSpawnLog) count() int        { v.mu.Lock(); defer v.mu.Unlock(); return len(v.args) }
func (v *vSpawnLog) at(i int) []string { v.mu.Lock(); defer v.mu.Unlock(); return v.args[i] }

func vNew(t *testing.T, cfg Config, log *vSpawnLog, scenarios ...string) *Session {
	t.Helper()
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	i := 0
	f := func(_ string, args ...string) *exec.Cmd {
		sc := scenarios[len(scenarios)-1]
		if i < len(scenarios) {
			sc = scenarios[i]
		}
		i++
		c := exec.Command(os.Args[0], "-test.run=TestVerifyFakePiProcess")
		c.Env = append(os.Environ(), vScen+"="+sc)
		if log != nil {
			log.mu.Lock()
			log.args = append(log.args, args)
			log.mu.Unlock()
		}
		return c
	}
	return NewSession(cfg, WithCommandFactory(f), WithLogger(logNew()))
}

func logNew() *log.Logger { return log.New(io.Discard, "", 0) }

func vChildren(t *testing.T) []string {
	t.Helper()
	var out []string
	seen := map[string]bool{}
	files, _ := filepath.Glob("/proc/" + strconv.Itoa(os.Getpid()) + "/task/*/children")
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, pid := range strings.Fields(string(raw)) {
			if seen[pid] {
				continue
			}
			seen[pid] = true
			if st, err := os.ReadFile("/proc/" + pid + "/stat"); err == nil {
				s := string(st)
				if i := strings.LastIndex(s, ")"); i >= 0 && i+2 < len(s) {
					out = append(out, pid+":"+string(s[i+2]))
				}
			}
		}
	}
	return out
}

// covers S1 (failure must surface, one retry only)
func TestVerifySecondCrashSurfacesError(t *testing.T) {
	sp := &vSpawnLog{}
	s := vNew(t, Config{Timeout: 5 * time.Second}, sp, "crash")
	defer s.Close()
	start := time.Now()
	if _, err := s.Prompt(context.Background(), "boom"); err == nil {
		t.Error("two consecutive crashes returned nil error")
	}
	if sp.count() != 2 {
		t.Errorf("spawns=%d, want exactly 2 (one retry)", sp.count())
	}
	if el := time.Since(start); el > 3000*time.Millisecond {
		t.Errorf("elapsed=%s: retry is looping", el)
	}
}

// covers S1 (no leaked/zombie children after timeout+restart and Close)
func TestVerifyNoZombieChildren(t *testing.T) {
	sp := &vSpawnLog{}
	s := vNew(t, Config{Timeout: 200 * time.Millisecond}, sp, "hang", "echo")
	if _, err := s.Prompt(context.Background(), "a"); err == nil {
		t.Fatal("expected timeout error")
	}
	if _, err := s.Prompt(context.Background(), "b"); err != nil {
		t.Fatalf("prompt after timeout: %v", err)
	}
	mid := vChildren(t)
	s.Close()
	after := vChildren(t)
	for _, c := range append(mid, after...) {
		if strings.HasSuffix(c, ":Z") {
			t.Errorf("unreaped child %s (cmd.Wait is never called)", c)
		}
	}
}

// covers S1 (a queued prompt keeps its own per-turn deadline)
func TestVerifyQueuedPromptKeepsFullDeadline(t *testing.T) {
	sp := &vSpawnLog{}
	s := vNew(t, Config{Timeout: 700 * time.Millisecond}, sp, "hang", "echo")
	defer s.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	var txt string
	var errB error
	go func() { defer wg.Done(); _, _ = s.Prompt(context.Background(), "largo") }()
	time.Sleep(20 * time.Millisecond)
	go func() { defer wg.Done(); txt, errB = s.Prompt(context.Background(), "espera") }()
	wg.Wait()
	if errB != nil || txt != "espera" {
		t.Errorf("queued prompt failed (%q, %v): its deadline was consumed while waiting for the mutex", txt, errB)
	}
}

// covers S7/S1 (arg cardinality, no --no-session)
func TestVerifyArgCardinality(t *testing.T) {
	count := func(a []string, f string) int {
		n := 0
		for _, x := range a {
			if x == f {
				n++
			}
		}
		return n
	}
	sp := &vSpawnLog{}
	s := vNew(t, Config{
		Tools:       "read,grep,find,ls",
		Extensions:  []string{"/ext/a.ts", "/ext/b.ts"},
		PromptFiles: []string{"/p.md", "/f.md"},
		WorkDir:     t.TempDir(),
	}, sp, "echo")
	if _, err := s.Prompt(context.Background(), "hola"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	s.Close()
	a := sp.at(0)
	if n := count(a, "-e"); n != 2 {
		t.Errorf("-e count=%d, want 2: %v", n, a)
	}
	if n := count(a, "--append-system-prompt"); n != 2 {
		t.Errorf("--append-system-prompt count=%d, want 2: %v", n, a)
	}
	if n := count(a, "--tools"); n != 1 {
		t.Errorf("--tools count=%d, want 1: %v", n, a)
	}
	for _, x := range a {
		if x == "--no-session" {
			t.Errorf("--no-session must never be passed: %v", a)
		}
	}
}

// covers S1 (no fd growth across timeout/restart cycles)
func TestVerifyNoFdLeakAcrossRestarts(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc")
	}
	n := func() int { e, _ := os.ReadDir("/proc/self/fd"); return len(e) }
	s := vNew(t, Config{Timeout: 80 * time.Millisecond}, nil, "hang", "hang", "hang", "echo")
	defer s.Close()
	base := n()
	for i := 0; i < 3; i++ {
		_, _ = s.Prompt(context.Background(), "x")
	}
	if after := n(); after > base+2 {
		t.Errorf("fd leak: %d -> %d after 3 restarts (stdout pipe never closed)", base, after)
	}
}

// covers S1 (null authoritative text is an error, not an empty success)
func TestVerifyNullTextIsAnError(t *testing.T) {
	s := vNew(t, Config{Timeout: 2 * time.Second}, nil, "null_text")
	defer s.Close()
	if txt, err := s.Prompt(context.Background(), "x"); err == nil {
		t.Errorf("null text returned %q with nil error", txt)
	}
}

// covers S1 (handled: the final text is never the streaming draft)
func TestVerifyHandledNeverReturnsDraft(t *testing.T) {
	s := vNew(t, Config{Timeout: 2 * time.Second}, nil, "handled")
	defer s.Close()
	var drafts []string
	txt, err := s.PromptStream(context.Background(), "/cmd", func(a string) { drafts = append(drafts, a) })
	if err != nil {
		t.Fatalf("handled prompt: %v", err)
	}
	if len(drafts) > 0 && txt == drafts[len(drafts)-1] {
		t.Errorf("handled returned the streaming draft %q as the final text", txt)
	}
	_ = fmt.Sprint()
}
