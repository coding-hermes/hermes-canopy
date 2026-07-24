package transport

import "sync"

// MessageQueue is a bounded ring buffer for offline message storage.
type MessageQueue struct {
	peerID   string
	buffer   []*Message
	head     int // next write position
	tail     int // next read position
	capacity int
	size     int
	mu       sync.Mutex
}

// NewMessageQueue creates a ring buffer with the given capacity. A non-positive
// capacity uses the default offline queue capacity.
func NewMessageQueue(peerID string, capacity int) *MessageQueue {
	if capacity <= 0 {
		capacity = DefaultQueueCapacity
	}
	return &MessageQueue{
		peerID:   peerID,
		buffer:   make([]*Message, capacity),
		capacity: capacity,
	}
}

// Enqueue adds a message to the ring buffer. If the buffer is full, the oldest
// message is dropped before the new message is written. The overflow is
// reported with ErrNoTransportAvailable after the new message is retained.
func (mq *MessageQueue) Enqueue(msg *Message) error {
	mq.mu.Lock()
	defer mq.mu.Unlock()
	mq.ensureInitialized()

	full := mq.size == mq.capacity
	if full {
		// Drop the oldest entry first. tail points at the oldest message and
		// head points at the slot that will be overwritten.
		mq.buffer[mq.tail] = nil
		mq.tail = (mq.tail + 1) % mq.capacity
		mq.size--
	}

	mq.buffer[mq.head] = msg
	mq.head = (mq.head + 1) % mq.capacity
	mq.size++
	if full {
		return ErrNoTransportAvailable
	}
	return nil
}

// Dequeue removes and returns the oldest message in FIFO order. It returns nil
// when the queue is empty.
func (mq *MessageQueue) Dequeue() *Message {
	mq.mu.Lock()
	defer mq.mu.Unlock()
	if mq.size == 0 {
		return nil
	}

	msg := mq.buffer[mq.tail]
	mq.buffer[mq.tail] = nil
	mq.tail = (mq.tail + 1) % mq.capacity
	mq.size--
	return msg
}

// Peek returns the oldest message without removing it. It returns nil when the
// queue is empty.
func (mq *MessageQueue) Peek() *Message {
	mq.mu.Lock()
	defer mq.mu.Unlock()
	if mq.size == 0 {
		return nil
	}
	return mq.buffer[mq.tail]
}

// Len returns the number of messages currently buffered.
func (mq *MessageQueue) Len() int {
	mq.mu.Lock()
	defer mq.mu.Unlock()
	return mq.size
}

// IsFull reports whether the queue has reached its capacity.
func (mq *MessageQueue) IsFull() bool {
	mq.mu.Lock()
	defer mq.mu.Unlock()
	return mq.capacity > 0 && mq.size == mq.capacity
}

// Drain dequeues all messages and returns them in FIFO order.
func (mq *MessageQueue) Drain() []*Message {
	mq.mu.Lock()
	defer mq.mu.Unlock()
	if mq.size == 0 {
		return nil
	}

	messages := make([]*Message, 0, mq.size)
	for mq.size > 0 {
		messages = append(messages, mq.buffer[mq.tail])
		mq.buffer[mq.tail] = nil
		mq.tail = (mq.tail + 1) % mq.capacity
		mq.size--
	}
	return messages
}

func (mq *MessageQueue) ensureInitialized() {
	if mq.capacity > 0 && len(mq.buffer) == mq.capacity {
		return
	}
	mq.capacity = DefaultQueueCapacity
	mq.buffer = make([]*Message, mq.capacity)
	mq.head = 0
	mq.tail = 0
	mq.size = 0
}

// Push is retained as a compatibility helper for callers from the initial
// transport-layer implementation. New callers should use Enqueue.
func (mq *MessageQueue) Push(msg *Message) {
	_ = mq.Enqueue(msg)
}

// PopAll is retained as a compatibility helper. New callers should use Drain.
func (mq *MessageQueue) PopAll() []*Message {
	return mq.Drain()
}

// Size is retained as a compatibility helper. New callers should use Len.
func (mq *MessageQueue) Size() int {
	return mq.Len()
}
