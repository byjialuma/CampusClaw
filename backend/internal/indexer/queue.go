package indexer

import (
	"context"
	"errors"
	"log"
	"sync"
)

// queueBuffer 是待索引队列容量；班级规模内绰绰有余，
// 溢出时拒绝入队，材料保持 pending，由下次启动回填兜底。
const queueBuffer = 4096

// ErrQueueFull 表示索引队列已满。
var ErrQueueFull = errors.New("索引队列已满")

// Queue 是单 worker 的异步索引队列：同一 handout_id 在队列中/处理中不重复入队。
type Queue struct {
	svc    *Service
	ch     chan int64
	queued map[int64]struct{}
	mu     sync.Mutex
	wg     sync.WaitGroup
}

// NewQueue 创建队列。
func NewQueue(svc *Service) *Queue {
	return &Queue{
		svc:    svc,
		ch:     make(chan int64, queueBuffer),
		queued: make(map[int64]struct{}),
	}
}

// Enqueue 将材料置为 pending 并投入异步队列；重复入队被静默合并。
func (q *Queue) Enqueue(ctx context.Context, handoutID int64) error {
	if err := q.svc.States.MarkPending(ctx, handoutID); err != nil {
		return err
	}

	q.mu.Lock()
	if _, dup := q.queued[handoutID]; dup {
		q.mu.Unlock()
		return nil
	}
	select {
	case q.ch <- handoutID:
		q.queued[handoutID] = struct{}{}
		q.mu.Unlock()
		return nil
	default:
		q.mu.Unlock()
		return ErrQueueFull
	}
}

// Start 启动唯一 worker，ctx 取消后停止领取新作业（不保证排空）。
func (q *Queue) Start(ctx context.Context) {
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case id := <-q.ch:
				q.runOne(id)
			}
		}
	}()
}

// Wait 等待 worker 退出（用于优雅停机测试）。
func (q *Queue) Wait() { q.wg.Wait() }

func (q *Queue) runOne(handoutID int64) {
	defer func() {
		q.mu.Lock()
		delete(q.queued, handoutID)
		q.mu.Unlock()
	}()
	// 作业独立于触发它的 HTTP 请求，使用后台上下文。
	if err := q.svc.IndexMaterial(context.Background(), handoutID); err != nil {
		log.Printf("材料 %d 索引失败: %v", handoutID, err)
	}
}
