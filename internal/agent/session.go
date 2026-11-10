package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

const (
	sessionBucket      = "sessions"
	sessionEventBucket = "session_events"
	sessionSeqWidth    = 8
)

// 会话状态。
const (
	StatusRunning = "running"
	StatusOK      = "ok"
	StatusFailed  = "failed"
)

// 事件类型。
const (
	EventUser      = "user"
	EventAssistant = "assistant"
	EventTool      = "tool"
	EventState     = "state"
	EventError     = "error"
	EventReport    = "report"
)

// Event 是会话事件（只追加）。
type Event struct {
	Seq      int       `json:"seq"`
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`
	Role     string    `json:"role,omitempty"`
	Content  string    `json:"content"`
	ToolName string    `json:"toolName,omitempty"`
	OK       *bool     `json:"ok,omitempty"`
}

// SessionMeta 是一个会话的元信息。
type SessionMeta struct {
	ID        string     `json:"id"`
	Worker    string     `json:"worker"`
	Title     string     `json:"title"`
	Input     string     `json:"input,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	Status    string     `json:"status"`
	Steps     int        `json:"steps"`
	Tokens    int        `json:"tokens"`
	Report    string     `json:"report,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// SessionDetail 是会话详情（API.md 第 5 节 session 方法）。
type SessionDetail struct {
	Session SessionMeta `json:"session"`
	Events  []Event     `json:"events"`
}

// SessionStore 用 contract.Store 持久化会话与事件：bucket=sessions / session_events，key 为递增序号。
type SessionStore struct {
	store contract.Store
	clock func() time.Time

	mu   sync.Mutex
	next map[string]int
}

// NewSessionStore 构造会话存储。
func NewSessionStore(store contract.Store, clock func() time.Time) *SessionStore {
	if clock == nil {
		clock = time.Now
	}
	return &SessionStore{store: store, clock: clock, next: map[string]int{}}
}

func eventKey(sessionID string, seq int) string {
	return sessionID + "/" + fmt.Sprintf("%0*d", sessionSeqWidth, seq)
}

// Create 落一条会话元信息。
func (s *SessionStore) Create(meta SessionMeta) error {
	if strings.TrimSpace(meta.ID) == "" {
		return fmt.Errorf("会话 id 不能为空")
	}
	if meta.Status == "" {
		meta.Status = StatusRunning
	}
	if meta.StartedAt.IsZero() {
		meta.StartedAt = s.clock()
	}
	buf, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("会话序列化失败：%w", err)
	}
	return s.store.Put(sessionBucket, meta.ID, buf)
}

// Update 覆盖会话元信息（只改元信息，事件仍然只追加）。
func (s *SessionStore) Update(meta SessionMeta) error {
	buf, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("会话序列化失败：%w", err)
	}
	return s.store.Put(sessionBucket, meta.ID, buf)
}

// Append 追加一条事件并分配递增序号。
func (s *SessionStore) Append(sessionID string, e Event) (Event, error) {
	if strings.TrimSpace(sessionID) == "" {
		return Event{}, fmt.Errorf("会话 id 不能为空")
	}
	s.mu.Lock()
	seq, ok := s.next[sessionID]
	if !ok {
		var err error
		seq, err = s.lastSeqLocked(sessionID)
		if err != nil {
			s.mu.Unlock()
			return Event{}, err
		}
	}
	seq++
	s.next[sessionID] = seq
	s.mu.Unlock()

	e.Seq = seq
	if e.Time.IsZero() {
		e.Time = s.clock()
	}
	buf, err := json.Marshal(e)
	if err != nil {
		return Event{}, fmt.Errorf("事件序列化失败：%w", err)
	}
	if err := s.store.Put(sessionEventBucket, eventKey(sessionID, seq), buf); err != nil {
		return Event{}, fmt.Errorf("追加事件失败：%w", err)
	}
	return e, nil
}

func (s *SessionStore) lastSeqLocked(sessionID string) (int, error) {
	items, err := s.store.List(sessionEventBucket, sessionID+"/")
	if err != nil {
		if err == contract.ErrNotFound {
			return 0, nil
		}
		return 0, fmt.Errorf("读取会话事件失败：%w", err)
	}
	max := 0
	for _, it := range items {
		idx := strings.LastIndex(it.Key, "/")
		if idx < 0 {
			continue
		}
		if n, err := strconv.Atoi(it.Key[idx+1:]); err == nil && n > max {
			max = n
		}
	}
	return max, nil
}

// Get 读取会话元信息与全部事件（按 seq 升序，即 Replay 顺序）。
func (s *SessionStore) Get(sessionID string) (SessionDetail, error) {
	buf, err := s.store.Get(sessionBucket, sessionID)
	if err != nil {
		return SessionDetail{}, fmt.Errorf("会话不存在：%s", sessionID)
	}
	var meta SessionMeta
	if err := json.Unmarshal(buf, &meta); err != nil {
		return SessionDetail{}, fmt.Errorf("会话数据损坏：%w", err)
	}
	items, err := s.store.List(sessionEventBucket, sessionID+"/")
	if err != nil && err != contract.ErrNotFound {
		return SessionDetail{}, fmt.Errorf("读取会话事件失败：%w", err)
	}
	events := make([]Event, 0, len(items))
	for _, it := range items {
		var e Event
		if err := json.Unmarshal(it.Value, &e); err != nil {
			continue
		}
		events = append(events, e)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	return SessionDetail{Session: meta, Events: events}, nil
}

// List 按开始时间倒序返回最近会话。
func (s *SessionStore) List(limit int) ([]SessionMeta, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	items, err := s.store.List(sessionBucket, "")
	if err != nil {
		if err == contract.ErrNotFound {
			return []SessionMeta{}, nil
		}
		return nil, fmt.Errorf("读取会话列表失败：%w", err)
	}
	out := make([]SessionMeta, 0, len(items))
	for _, it := range items {
		var m SessionMeta
		if err := json.Unmarshal(it.Value, &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SessionSummary 是会话列表项（API.md 第 5 节 sessions 方法）。
type SessionSummary struct {
	ID        string `json:"id"`
	Worker    string `json:"worker"`
	Title     string `json:"title"`
	StartedAt string `json:"startedAt"`
	Status    string `json:"status"`
	Tokens    int    `json:"tokens"`
}

// Summaries 返回会话列表视图。
func (s *SessionStore) Summaries(limit int) ([]SessionSummary, error) {
	metas, err := s.List(limit)
	if err != nil {
		return nil, err
	}
	out := make([]SessionSummary, 0, len(metas))
	for _, m := range metas {
		out = append(out, SessionSummary{
			ID:        m.ID,
			Worker:    m.Worker,
			Title:     m.Title,
			StartedAt: m.StartedAt.Format(time.RFC3339),
			Status:    m.Status,
			Tokens:    m.Tokens,
		})
	}
	return out, nil
}

// NewSessionID 生成会话 id：时间戳 + 随机后缀，便于按时间排序又避免碰撞。
func NewSessionID(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s", now.Format("20060102-150405"), hex.EncodeToString(b[:]))
}

// nl 是换行符（用 rune 构造，避免源码里的转义序列被工具链改写）。
var nl = string(rune(10))
