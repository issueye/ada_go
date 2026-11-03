package control

// 异步调用（长任务）——/v1/call 的补充，不是替代。
//
//	POST   /v1/calls        启动任务，立即返回 {ok,id,status:"running",timeout_ms}
//	GET    /v1/calls/{id}   查询 {ok,job:{id,name,status,started_at,ended_at,elapsed_ms,result?}}
//	DELETE /v1/calls/{id}   取消（任务收到 ctx 取消，结果按 cancelled 收尾）
//
// 为什么需要它：/v1/call 是同步接口，响应头要等动作跑完才发。任何带「响应头超时」的
// 客户端（ada-go 出网桥 15s、nginx proxy_read_timeout、curl --max-time 等）都会在长
// 任务上误判超时；客户端一断，服务端还会把脚本记成 script_timeout（错误归因误导）。
// 异步任务与请求解耦后：
//   - 启动请求毫秒级返回，不会再撞客户端的响应头超时；
//   - 客户端断开不影响任务，结果留在任务里等轮询；
//   - client 归因仍是调用方给的值，不会退化成 cli-<pid>。
//
// 边界：任务数上限 64（含已结束，按 TTL 清理）、并发上限 4、单任务上限由
// callTimeoutFor 决定（脚本用 Script.MaxTimeoutMS）。超过并发上限返回 429。

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

type jobStatus string

const (
	jobRunning   jobStatus = "running"
	jobDone      jobStatus = "done"
	jobCancelled jobStatus = "cancelled"
)

const (
	defaultMaxJobs    = 64
	defaultMaxRunning = 4
	defaultJobTTL     = 10 * time.Minute
)

// asyncJob 是一个后台任务。ctx/cancel 不参与 JSON 序列化。
type asyncJob struct {
	id     string
	name   string
	client string

	started time.Time
	ended   time.Time
	status  jobStatus
	result  callResult

	ctx    context.Context
	cancel context.CancelFunc
}

// JobView 是 /v1/calls/{id} 的 job 字段。
type JobView struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Client    string      `json:"client,omitempty"`
	Status    string      `json:"status"`
	StartedAt time.Time   `json:"started_at"`
	EndedAt   *time.Time  `json:"ended_at,omitempty"`
	ElapsedMS int64       `json:"elapsed_ms"`
	Result    *callResult `json:"result,omitempty"`
}

// jobStore 保存任务；所有方法都持锁，任务量很小（≤64）。
type jobStore struct {
	mu      sync.Mutex
	jobs    map[string]*asyncJob
	order   []string
	max     int
	maxRun  int
	running int
	ttl     time.Duration
	now     func() time.Time
}

func newJobStore() *jobStore {
	return &jobStore{
		jobs: map[string]*asyncJob{}, max: defaultMaxJobs, maxRun: defaultMaxRunning,
		ttl: defaultJobTTL, now: time.Now,
	}
}

var errJobBusy = fmt.Errorf("too many running browser tasks")

// create 分配 id 与期限，并返回一个已绑定 ctx 的任务（调用方负责起 goroutine）。
func (s *jobStore) create(name, client string, timeout time.Duration) (*asyncJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.now())
	if s.running >= s.maxRun {
		return nil, errJobBusy
	}
	if len(s.order) >= s.max {
		s.evictLocked()
		if len(s.order) >= s.max {
			return nil, fmt.Errorf("too many finished tasks; retry after cleanup")
		}
	}
	id, err := newJobID()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	j := &asyncJob{
		id: id, name: name, client: client, started: s.now(), status: jobRunning,
		ctx: ctx, cancel: cancel,
	}
	s.jobs[id] = j
	s.order = append(s.order, id)
	s.running++
	return j, nil
}

// finish 收尾：只在任务仍是 running 时生效（cancel 可能先到）。
func (s *jobStore) finish(id string, res callResult, status jobStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil || j.status != jobRunning {
		return
	}
	j.status = status
	j.result = res
	j.ended = s.now()
	j.cancel()
	s.running--
}

// get 返回任务快照；结果只在结束时出现。
func (s *jobStore) get(id string) (JobView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil {
		return JobView{}, false
	}
	return viewOf(j, s.now()), true
}

// cancel 立即把任务标记成 cancelled 并取消 ctx；runner 的 finish 会被忽略。
func (s *jobStore) cancel(id string) (JobView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil {
		return JobView{}, false
	}
	if j.status == jobRunning {
		j.status = jobCancelled
		j.ended = s.now()
		j.cancel()
		s.running--
	}
	return viewOf(j, s.now()), true
}

// cancelAll 用于 Server.Close：让所有在跑的任务尽快退出。
func (s *jobStore) cancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.status == jobRunning {
			j.cancel()
			j.status = jobCancelled
			j.ended = s.now()
			s.running--
		}
	}
}

func viewOf(j *asyncJob, now time.Time) JobView {
	v := JobView{
		ID: j.id, Name: j.name, Client: j.client, Status: string(j.status),
		StartedAt: j.started,
	}
	end := j.ended
	if j.status == jobRunning {
		end = now
	} else if j.ended.IsZero() {
		end = j.started
	}
	v.ElapsedMS = end.Sub(j.started).Milliseconds()
	if !j.ended.IsZero() {
		t := j.ended
		v.EndedAt = &t
	}
	if j.status != jobRunning {
		res := j.result
		v.Result = &res
	}
	return v
}

// sweepLocked 清理超过 TTL 的已结束任务；调用方持锁。
func (s *jobStore) sweepLocked(now time.Time) {
	if s.ttl <= 0 {
		return
	}
	kept := s.order[:0]
	for _, id := range s.order {
		j := s.jobs[id]
		if j == nil {
			continue
		}
		if j.status != jobRunning && !j.ended.IsZero() && now.Sub(j.ended) > s.ttl {
			delete(s.jobs, id)
			continue
		}
		kept = append(kept, id)
	}
	s.order = kept
}

// evictLocked 在容量满时先丢最老的已结束任务；调用方持锁。
func (s *jobStore) evictLocked() {
	for i, id := range s.order {
		j := s.jobs[id]
		if j == nil || j.status != jobRunning {
			delete(s.jobs, id)
			s.order = append(s.order[:i], s.order[i+1:]...)
			return
		}
	}
}

func newJobID() (string, error) {
	var b [9]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "job-" + base64.RawURLEncoding.EncodeToString(b[:]), nil
}
