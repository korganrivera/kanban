package httpapi

import "sync"

type broker struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]struct{}
}

func newBroker() *broker {
	return &broker{subscribers: make(map[chan struct{}]struct{})}
}

func (b *broker) subscribe() (<-chan struct{}, func()) {
	channel := make(chan struct{}, 1)
	b.mu.Lock()
	b.subscribers[channel] = struct{}{}
	b.mu.Unlock()
	return channel, func() {
		b.mu.Lock()
		delete(b.subscribers, channel)
		b.mu.Unlock()
	}
}

func (b *broker) publish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for channel := range b.subscribers {
		select {
		case channel <- struct{}{}:
		default:
		}
	}
}
