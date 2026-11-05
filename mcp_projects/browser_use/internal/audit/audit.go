// Package audit writes one JSONL line per tool call.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one tool call.
type Entry struct {
	TS     time.Time `json:"ts"`
	Tool   string    `json:"tool"`
	Tab    string    `json:"tab,omitempty"`
	URL    string    `json:"url,omitempty"`
	Client string    `json:"client,omitempty"`
	OK     bool      `json:"ok"`
	Code   string    `json:"code,omitempty"`
	MS     int64     `json:"ms"`
	Args   any       `json:"args,omitempty"`
	Script string    `json:"script,omitempty"`
}

// Log writes entries asynchronously.
type Log struct {
	dir  string
	ch   chan Entry
	done chan struct{}
	once sync.Once

	mu   sync.Mutex
	day  string
	file *os.File
}

// New opens the audit directory and starts the writer.
func New(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	l := &Log{dir: dir, ch: make(chan Entry, 256), done: make(chan struct{})}
	go l.run()
	return l, nil
}

// Write queues an entry. A full queue drops it rather than blocking a tool.
func (l *Log) Write(e Entry) {
	if l == nil {
		return
	}
	select {
	case l.ch <- e:
	default:
	}
}

// Close flushes and closes the current file.
func (l *Log) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		close(l.ch)
		<-l.done
		l.mu.Lock()
		if l.file != nil {
			_ = l.file.Close()
			l.file = nil
		}
		l.mu.Unlock()
	})
}

func (l *Log) run() {
	defer close(l.done)
	for e := range l.ch {
		l.write(e)
	}
}

func (l *Log) write(e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	day := e.TS.Format("20060102")
	if day == "" {
		day = time.Now().Format("20060102")
	}
	if l.file == nil || l.day != day {
		if l.file != nil {
			_ = l.file.Close()
		}
		f, err := os.OpenFile(filepath.Join(l.dir, day+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		l.file = f
		l.day = day
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = l.file.Write(append(b, '\n'))
}

// RedactArgs returns the arguments of a tool with secrets and bulk data
// removed.
func RedactArgs(tool string, raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"bytes": len(raw)}
	}
	for _, k := range []string{"data", "image"} {
		delete(m, k)
	}
	if v, ok := m["text"]; ok {
		m["text_len"] = len(fmt.Sprint(v))
		delete(m, "text")
	}
	if v, ok := m["expression"]; ok {
		s := fmt.Sprint(v)
		if len(s) > 200 {
			s = s[:200] + "…"
		}
		m["expression"] = s
	}
	if v, ok := m["source"]; ok {
		s := fmt.Sprint(v)
		m["source_bytes"] = len(s)
		delete(m, "source")
	}
	if v, ok := m["values"]; ok {
		m["values"] = v
	}
	if tool == "cookies" {
		if v, ok := m["value"]; ok {
			m["value_len"] = len(fmt.Sprint(v))
			m["value"] = "***"
		}
		if arr, ok := m["cookies"].([]any); ok {
			for _, item := range arr {
				if c, ok := item.(map[string]any); ok {
					if v, ok := c["value"]; ok {
						c["value_len"] = len(fmt.Sprint(v))
						c["value"] = "***"
					}
				}
			}
		}
	}
	if tool == "script" {
		if a, ok := m["args"].(map[string]any); ok {
			keys := make([]string, 0, len(a))
			for k := range a {
				keys = append(keys, k)
			}
			m["args_keys"] = keys
			delete(m, "args")
		}
	}
	if tool == "storage" {
		// storage 的 value 与 cookie 一样敏感(登录态/session token)。
		if v, ok := m["value"]; ok {
			m["value_len"] = len(fmt.Sprint(v))
			m["value"] = "***"
		}
	}
	return m
}
