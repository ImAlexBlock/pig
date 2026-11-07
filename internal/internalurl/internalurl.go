// Package internalurl resolves the pig internal URI schemes:
//
//	artifact://<id>
//	skill://<name>[/path]
//	session://<id> | session://current
//	file://<abs>
//
// Every scheme accepts an optional trailing selector: `:N-M`, `:N-`, `:-N`,
// `:raw`, or `:raw:N-M`.
package internalurl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alexblock/pig/internal/store"
)

// Doc kinds.
const (
	KindText   = "text"
	KindBinary = "binary"
	KindImage  = "image"
	KindDir    = "dir"
)

// Doc is the resolved content of an internal URI.
type Doc struct {
	URI        string
	Kind       string
	Text       string
	Data       []byte
	Mime       string
	ArtifactID string
	Bytes      int
	Raw        bool
	Start      int // 1-based inclusive line, 0 = unset
	End        int // 1-based inclusive line, 0 = unset
}

// Resolver resolves internal URIs.
type Resolver struct {
	Store     *store.Store
	SkillDir  func(name string) (string, bool)
	SessionID string
}

// IsInternal reports whether uri uses an internal scheme.
func IsInternal(uri string) bool {
	for _, p := range []string{"artifact://", "skill://", "session://", "file://"} {
		if strings.HasPrefix(uri, p) {
			return true
		}
	}
	return false
}

// SplitSelector strips and parses trailing selectors. Selectors are consumed
// from the right and may combine, e.g. `:raw:10-20`.
func SplitSelector(uri string) (base string, raw bool, start, end int) {
	base = uri
	for {
		i := strings.LastIndex(base, ":")
		if i < 0 {
			return
		}
		sel := base[i+1:]
		if strings.EqualFold(sel, "raw") {
			raw = true
			base = base[:i]
			continue
		}
		s, e, ok := parseRange(sel)
		if !ok {
			return
		}
		start, end = s, e
		base = base[:i]
	}
}

func parseRange(s string) (start, end int, ok bool) {
	if s == "" {
		return 0, 0, false
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		lo, hi := s[:i], s[i+1:]
		if lo == "" && hi == "" {
			return 0, 0, false
		}
		if lo != "" {
			v, err := strconv.Atoi(lo)
			if err != nil || v < 0 {
				return 0, 0, false
			}
			start = v
		}
		if hi != "" {
			v, err := strconv.Atoi(hi)
			if err != nil || v < 0 {
				return 0, 0, false
			}
			end = v
		}
		return start, end, true
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return 0, 0, false
	}
	return v, v, true
}

// Open resolves a URI.
func (r *Resolver) Open(uri string) (*Doc, error) {
	base, raw, start, end := SplitSelector(uri)
	switch {
	case strings.HasPrefix(base, "artifact://"):
		return r.openArtifact(uri, strings.TrimPrefix(base, "artifact://"), raw, start, end)
	case strings.HasPrefix(base, "skill://"):
		return r.openSkill(uri, strings.TrimPrefix(base, "skill://"), raw, start, end)
	case strings.HasPrefix(base, "session://"):
		return r.openSession(uri, strings.TrimPrefix(base, "session://"), raw, start, end)
	case strings.HasPrefix(base, "file://"):
		return r.openFile(uri, strings.TrimPrefix(base, "file://"), raw, start, end)
	}
	return nil, fmt.Errorf("unknown internal uri: %s", uri)
}

func (r *Resolver) openArtifact(uri, id string, raw bool, start, end int) (*Doc, error) {
	if r.Store == nil {
		return nil, fmt.Errorf("artifact not found: %s", id)
	}
	meta, data, err := r.Store.ReadArtifact(id)
	if err != nil {
		return nil, fmt.Errorf("artifact not found: %s", id)
	}
	d := &Doc{
		URI:        uri,
		ArtifactID: meta.ID,
		Mime:       meta.Mime,
		Data:       data,
		Bytes:      len(data),
		Raw:        raw,
		Start:      start,
		End:        end,
	}
	switch {
	case isImageMime(meta.Mime):
		d.Kind = KindImage
	case isBinary(data):
		d.Kind = KindBinary
	default:
		d.Kind = KindText
		d.Text = string(data)
	}
	return d, nil
}

func (r *Resolver) openSkill(uri, rest string, raw bool, start, end int) (*Doc, error) {
	if strings.Contains(rest, "..") || strings.HasPrefix(rest, "/") {
		return nil, fmt.Errorf("skill path: invalid %q", rest)
	}
	name, sub, _ := strings.Cut(rest, "/")
	if name == "" {
		return nil, fmt.Errorf("skill path: missing name")
	}
	if r.SkillDir == nil {
		return nil, fmt.Errorf("skill not found: %s", name)
	}
	dir, ok := r.SkillDir(name)
	if !ok {
		return nil, fmt.Errorf("skill not found: %s", name)
	}
	target := dir
	if sub != "" {
		if strings.Contains(sub, "..") || strings.HasPrefix(sub, "/") {
			return nil, fmt.Errorf("skill path: invalid %q", sub)
		}
		target = filepath.Join(dir, filepath.Clean("/" + sub)[1:])
	}
	if rel, err := filepath.Rel(dir, target); err != nil || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("skill path: invalid %q", rest)
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("skill file not found: %s", rest)
	}
	if info.IsDir() {
		entries, err := os.ReadDir(target)
		if err != nil {
			return nil, fmt.Errorf("skill dir: %v", err)
		}
		var sb strings.Builder
		for _, e := range entries {
			if e.IsDir() {
				sb.WriteString(e.Name() + "/\n")
				continue
			}
			sb.WriteString(e.Name() + "\n")
		}
		return &Doc{URI: uri, Kind: KindDir, Text: sb.String(), Raw: raw, Start: start, End: end}, nil
	}
	if info.Size() > 1<<20 {
		return nil, fmt.Errorf("skill file too large: %s", rest)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("skill file: %v", err)
	}
	return &Doc{URI: uri, Kind: KindText, Text: string(data), Bytes: len(data), Raw: raw, Start: start, End: end}, nil
}

func (r *Resolver) openFile(uri, p string, raw bool, start, end int) (*Doc, error) {
	if !filepath.IsAbs(p) {
		return nil, fmt.Errorf("file uri: must be absolute (%s)", p)
	}
	return ReadFileDoc(uri, p, raw, start, end)
}

// ReadFileDoc reads a filesystem path into a Doc.
func ReadFileDoc(uri, p string, raw bool, start, end int) (*Doc, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("file not found: %s", p)
	}
	if info.IsDir() {
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, fmt.Errorf("dir: %v", err)
		}
		var sb strings.Builder
		for _, e := range entries {
			if e.IsDir() {
				sb.WriteString(e.Name() + "/\n")
				continue
			}
			sb.WriteString(e.Name() + "\n")
		}
		return &Doc{URI: uri, Kind: KindDir, Text: sb.String(), Raw: raw, Start: start, End: end}, nil
	}
	if info.Size() > 64<<20 {
		return nil, fmt.Errorf("file too large: %s", p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read: %v", err)
	}
	d := &Doc{URI: uri, Bytes: len(data), Raw: raw, Start: start, End: end, Mime: mimeByExt(p)}
	if isImageMime(d.Mime) {
		d.Kind = KindImage
		d.Data = data
		return d, nil
	}
	if isBinary(data) {
		d.Kind = KindBinary
		d.Data = data
		return d, nil
	}
	d.Kind = KindText
	d.Text = string(data)
	return d, nil
}

func (r *Resolver) openSession(uri, id string, raw bool, start, end int) (*Doc, error) {
	if r.Store == nil {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	if id == "" || id == "current" {
		id = r.SessionID
	}
	if id == "" {
		return nil, fmt.Errorf("session: no active session")
	}
	sess, err := r.Store.GetSession(id)
	if err != nil {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	entries, err := r.Store.AllEntries(sess.ID)
	if err != nil {
		return nil, fmt.Errorf("session: %v", err)
	}
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(RenderEntry(e))
		sb.WriteString("\n")
	}
	return &Doc{URI: uri, Kind: KindText, Text: sb.String(), Raw: raw, Start: start, End: end}, nil
}

// RenderEntry formats one entry for session:// output.
func RenderEntry(e *store.Entry) string {
	switch e.Kind {
	case store.KindMessage:
		m := e.Message()
		label := e.Role
		if label == "" {
			label = "assistant"
		}
		return fmt.Sprintf("#%d %s: %s", e.Seq, label, m.Text)
	case store.KindToolCall:
		c := e.ToolCall()
		args := compactJSON(c.Args)
		return fmt.Sprintf("#%d tool %s: %s", e.Seq, c.Name, args)
	case store.KindToolResult:
		r := e.ToolResult()
		return fmt.Sprintf("#%d tool %s: %s", e.Seq, r.Name, r.Text)
	case store.KindNotice:
		return fmt.Sprintf("#%d notice: %s", e.Seq, e.Notice().Text)
	case store.KindTodo:
		t := e.Todo()
		var parts []string
		for _, ph := range t.Phases {
			for _, it := range ph.Items {
				parts = append(parts, ph.Name+"/"+it.Text+" ["+it.Status+"]")
			}
		}
		return fmt.Sprintf("#%d todo: %s", e.Seq, strings.Join(parts, "; "))
	case store.KindJobResult:
		j := e.JobResult()
		return fmt.Sprintf("#%d job %s %s: %s", e.Seq, j.JobID, j.Status, j.Text)
	case store.KindCompaction:
		return fmt.Sprintf("#%d compaction: %s", e.Seq, e.Compaction().CompactionID)
	}
	return fmt.Sprintf("#%d %s", e.Seq, e.Kind)
}

func compactJSON(b json.RawMessage) string {
	if len(b) == 0 {
		return "{}"
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(b)
	}
	return string(out)
}

func isImageMime(m string) bool {
	switch m {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

func mimeByExt(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".md":
		return "text/markdown"
	case ".json":
		return "application/json"
	}
	return "text/plain"
}

func isBinary(data []byte) bool {
	n := len(data)
	if n > 8192 {
		n = 8192
	}
	for i := range n {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// SelectLines applies a 1-based inclusive line range. It returns the selected
// text and the 1-based line number of the first selected line.
func SelectLines(text string, start, end int) (string, int) {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if start < 1 {
		start = 1
	}
	if end < 1 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		return "", start
	}
	return strings.Join(lines[start-1:end], "\n"), start
}

// RenderLinePrefixed renders text as `%6d| ` prefixed lines (unless raw).
func RenderLinePrefixed(text string, firstLine int, raw bool) string {
	if raw {
		return text
	}
	lines := strings.Split(text, "\n")
	var sb strings.Builder
	sb.Grow(len(text) + len(lines)*8)
	for i, ln := range lines {
		sb.WriteString(fmt.Sprintf("%6d| %s", firstLine+i, ln))
		if i < len(lines)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
