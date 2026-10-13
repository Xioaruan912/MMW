package httpapi

import (
	"sync"
	"time"
)

// 登录限流策略。
const (
	loginWindow      = 15 * time.Minute // 统计窗口
	loginMaxFailures = 5                // 窗口内允许的连续失败次数
	loginBaseLock    = 15 * time.Minute // 首次锁定时长
	loginMaxLock     = 24 * time.Hour   // 锁定时长上限
)

type failBucket struct {
	failures  int
	firstFail time.Time
	lastFail  time.Time
	lockUntil time.Time
	lockLevel int // 已被锁定次数，用于指数退避
}

// loginLimiter 是进程内的登录防爆破限流器。
//
// 同时按 IP 与用户名两个维度计数：只按 IP 可被分布式绕过，
// 只按用户名会让攻击者用少量用户名把正常用户锁死。
type loginLimiter struct {
	mu     sync.Mutex
	byIP   map[string]*failBucket
	byUser map[string]*failBucket
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		byIP:   make(map[string]*failBucket),
		byUser: make(map[string]*failBucket),
	}
}

// Check 返回是否允许尝试登录；被锁定时同时返回剩余时长。
func (l *loginLimiter) Check(ip, username string) (bool, time.Duration) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked(now)

	for _, b := range []*failBucket{l.byIP[ip], l.byUser[username]} {
		if b == nil {
			continue
		}
		if now.Before(b.lockUntil) {
			return false, b.lockUntil.Sub(now)
		}
	}
	return true, 0
}

// Fail 记录一次失败。
func (l *loginLimiter) Fail(ip, username string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bump(l.byIP, ip, now)
	l.bump(l.byUser, username, now)
}

// Success 清空该 IP 与该用户名的失败计数。
func (l *loginLimiter) Success(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
	delete(l.byUser, username)
}

func (l *loginLimiter) bump(m map[string]*failBucket, key string, now time.Time) {
	if key == "" {
		return
	}
	b := m[key]
	if b == nil || now.Sub(b.firstFail) > loginWindow {
		m[key] = &failBucket{failures: 1, firstFail: now, lastFail: now}
		return
	}
	b.failures++
	b.lastFail = now
	if b.failures >= loginMaxFailures {
		b.lockLevel++
		lock := loginBaseLock
		for i := 1; i < b.lockLevel; i++ {
			lock *= 2
			if lock >= loginMaxLock {
				lock = loginMaxLock
				break
			}
		}
		b.lockUntil = now.Add(lock)
		b.failures = 0
		b.firstFail = now
	}
}

// gcLocked 清理长期无活动的条目，避免内存无界增长。调用方需持锁。
func (l *loginLimiter) gcLocked(now time.Time) {
	const ttl = 48 * time.Hour
	for k, b := range l.byIP {
		if now.Sub(b.lastFail) > ttl && now.After(b.lockUntil) {
			delete(l.byIP, k)
		}
	}
	for k, b := range l.byUser {
		if now.Sub(b.lastFail) > ttl && now.After(b.lockUntil) {
			delete(l.byUser, k)
		}
	}
}
