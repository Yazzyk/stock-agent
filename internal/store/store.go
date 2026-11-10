// Package store 是本地持久化实现：单文件 sqlite（modernc.org/sqlite，纯 Go，无需 CGO）。
//
// 数据模型刻意做成最小 KV：bucket/key/value(blob)/updated_at。
// 这样插件不用为「加一张表」改 schema，迁移只发生在建库那一次。
// 并发安全由 database/sql 连接池 + WAL + busy_timeout 保证，Close 之后所有调用返回错误。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 注册纯 Go sqlite 驱动

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// ErrClosed 表示 Store 已关闭。
var ErrClosed = errors.New("store: 已关闭")

// DB 实现 contract.Store。所有导出方法可并发调用。
type DB struct {
	path string
	sql  *sql.DB

	mu     sync.RWMutex
	closed bool
}

// Open 打开（必要时创建）sqlite 库文件，并完成建表迁移。
// 库文件不存在时自动创建，父目录不存在时一并创建；WAL 模式在首次打开后写入库文件，之后持续生效。
func Open(path string) (*DB, error) {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return nil, errors.New("store: 库文件路径不能为空")
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return nil, fmt.Errorf("store: 解析库文件路径失败: %w", err)
	}
	if dir := filepath.Dir(abs); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store: 创建数据目录失败: %w", err)
		}
	}

	dsn := "file:" + url.PathEscape(abs) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: 打开库文件失败: %w", err)
	}
	// WAL 下读并发、写串行：连接数放宽到 8 足够覆盖插件并行查询，又不会压出写锁风暴。
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)

	s := &DB{path: abs, sql: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Path 返回库文件的绝对路径（便于日志与「打开数据目录」入口展示）。
func (s *DB) Path() string { return s.path }

func (s *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS kv (
		bucket TEXT NOT NULL,
	key    TEXT NOT NULL,
	value  BLOB NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (bucket, key)
)`,
		// 主键 (bucket,key) 已经承担「前缀查询按 key 升序」，这里再补一条按时间被检索的索引，
		// 供清理/导出按更新时间扫描，避免全表。
		`CREATE INDEX IF NOT EXISTS idx_kv_bucket_updated ON kv (bucket, updated_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.sql.Exec(stmt); err != nil {
			return fmt.Errorf("store: 建表迁移失败: %w", err)
		}
	}
	if _, err := s.sql.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("store: 开启 WAL 失败: %w", err)
	}
	return nil
}

// usable 在关闭后统一拒绝调用，避免 nil 指针 panic。
func (s *DB) usable() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	return nil
}

// Get 读取一个键；键不存在返回 contract.ErrNotFound。
func (s *DB) Get(bucket, key string) ([]byte, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	if bucket == "" || key == "" {
		return nil, errors.New("store: bucket 与 key 不能为空")
	}
	var value []byte
	err := s.sql.QueryRow("SELECT value FROM kv WHERE bucket = ? AND key = ?", bucket, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, contract.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读取 %s/%s 失败: %w", bucket, key, err)
	}
	return value, nil
}

// Put 写入（upsert）一个键。
func (s *DB) Put(bucket, key string, val []byte) error {
	if err := s.usable(); err != nil {
		return err
	}
	if bucket == "" || key == "" {
		return errors.New("store: bucket 与 key 不能为空")
	}
	if val == nil {
		val = []byte{}
	}
	_, err := s.sql.Exec(
		`INSERT INTO kv (bucket, key, value, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(bucket, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		bucket, key, val, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: 写入 %s/%s 失败: %w", bucket, key, err)
	}
	return nil
}

// Delete 删除一个键；键不存在也返回 nil（删除是幂等的）。
func (s *DB) Delete(bucket, key string) error {
	if err := s.usable(); err != nil {
		return err
	}
	if bucket == "" || key == "" {
		return errors.New("store: bucket 与 key 不能为空")
	}
	if _, err := s.sql.Exec("DELETE FROM kv WHERE bucket = ? AND key = ?", bucket, key); err != nil {
		return fmt.Errorf("store: 删除 %s/%s 失败: %w", bucket, key, err)
	}
	return nil
}

// List 返回 bucket 下以 prefix 开头的全部键值，按 key 字节序升序。
//
// 用范围扫描（key >= prefix 且 key < prefixNext）而不是 LIKE：
// LIKE 需要转义 % 和 _，且前缀含通配符时语义会错；范围扫描还能直接走主键索引。
func (s *DB) List(bucket, prefix string) ([]contract.KVItem, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	if bucket == "" {
		return nil, errors.New("store: bucket 不能为空")
	}

	var (
		rows *sql.Rows
		err  error
	)
	if next, ok := prefixNext(prefix); ok {
		rows, err = s.sql.Query(
			"SELECT key, value FROM kv WHERE bucket = ? AND key >= ? AND key < ? ORDER BY key ASC",
			bucket, prefix, next)
	} else {
		// prefix 为空或已无上界（全 0xff）时退化为整桶按序扫描。
		rows, err = s.sql.Query("SELECT key, value FROM kv WHERE bucket = ? ORDER BY key ASC", bucket)
	}
	if err != nil {
		return nil, fmt.Errorf("store: 列举 %s/%s 失败: %w", bucket, prefix, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]contract.KVItem, 0, 16)
	for rows.Next() {
		var item contract.KVItem
		if err := rows.Scan(&item.Key, &item.Value); err != nil {
			return nil, fmt.Errorf("store: 读取列举结果失败: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 列举 %s/%s 失败: %w", bucket, prefix, err)
	}
	return out, nil
}

// Close 关闭库文件；重复调用返回 nil。
func (s *DB) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if err := s.sql.Close(); err != nil {
		return fmt.Errorf("store: 关闭库文件失败: %w", err)
	}
	return nil
}

// prefixNext 计算前缀开区间上界：把最后一个非 0xff 字节加一。
// 空前缀或全 0xff 前缀没有有效上界，返回 ok=false。
func prefixNext(prefix string) (string, bool) {
	if prefix == "" {
		return "", false
	}
	buf := []byte(prefix)
	for i := len(buf) - 1; i >= 0; i-- {
		if buf[i] < 0xff {
			buf[i]++
			return string(buf[:i+1]), true
		}
	}
	return "", false
}
