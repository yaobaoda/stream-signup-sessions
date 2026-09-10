package streamauth

import (
	"sort"
	"sync"
	"time"
)

// IngestJob tracks one uploaded master through processing to a delivery path
// the creator can hand to a player.
type IngestJob struct {
	ID          string    `json:"id"`
	CreatorID   string    `json:"creator_id"`
	Title       string    `json:"title"`
	SourceKey   string    `json:"source_key"`
	State       string    `json:"state"`
	DeliveryURL string    `json:"delivery_url,omitempty"`
	QueuedAt    time.Time `json:"queued_at"`
}

// Queue is an in-process work queue: enqueue on upload, drain in the worker
// goroutine, mark ready when the rendition is addressable.
type Queue struct {
	mu      sync.Mutex
	jobs    map[string]*IngestJob
	idem    map[string]string
	pending []string
	CDNBase string
	Now     func() time.Time
}

func NewQueue(cdnBase string) *Queue {
	return &Queue{
		jobs:    map[string]*IngestJob{},
		idem:    map[string]string{},
		CDNBase: cdnBase,
		Now:     time.Now,
	}
}

// Enqueue accepts a master for a creator. A repeated upload id returns the job
// already queued, so a retried request never doubles the processing bill.
func (q *Queue) Enqueue(creatorID, title, sourceKey, uploadID string) *IngestJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	if uploadID != "" {
		if id, seen := q.idem[uploadID]; seen {
			return q.jobs[id]
		}
	}
	job := &IngestJob{
		ID:        "job_" + randomHex(8),
		CreatorID: creatorID,
		Title:     title,
		SourceKey: sourceKey,
		State:     "queued",
		QueuedAt:  q.Now(),
	}
	q.jobs[job.ID] = job
	q.pending = append(q.pending, job.ID)
	if uploadID != "" {
		q.idem[uploadID] = job.ID
	}
	return job
}

// Drain moves every queued job to ready and stamps its delivery path.
func (q *Queue) Drain() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	n := 0
	for _, id := range q.pending {
		job := q.jobs[id]
		job.State = "ready"
		job.DeliveryURL = q.CDNBase + "/" + job.CreatorID + "/" + job.ID + "/master.m3u8"
		n++
	}
	q.pending = nil
	return n
}

// List returns a creator's jobs, newest first.
func (q *Queue) List(creatorID string) []IngestJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := []IngestJob{}
	for _, job := range q.jobs {
		if job.CreatorID == creatorID {
			out = append(out, *job)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QueuedAt.After(out[j].QueuedAt) })
	return out
}
