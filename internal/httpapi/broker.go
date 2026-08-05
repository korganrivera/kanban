package httpapi

import "sync"

type broker struct {
	mu          sync.Mutex
	subscribers map[*subscriber]struct{}
}

type subscriber struct {
	updates     chan struct{}
	username    string
	sessionHash string
}

func newBroker() *broker {
	return &broker{subscribers: make(map[*subscriber]struct{})}
}

func (b *broker) subscribe(username, sessionHash string) (<-chan struct{}, func()) {
	subscription := &subscriber{
		updates:     make(chan struct{}, 1),
		username:    username,
		sessionHash: sessionHash,
	}
	b.mu.Lock()
	b.subscribers[subscription] = struct{}{}
	b.mu.Unlock()
	return subscription.updates, func() {
		b.mu.Lock()
		delete(b.subscribers, subscription)
		b.mu.Unlock()
	}
}

func (b *broker) publish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for subscription := range b.subscribers {
		select {
		case subscription.updates <- struct{}{}:
		default:
		}
	}
}

func (b *broker) disconnectUser(username string) {
	b.disconnect(func(subscription *subscriber) bool { return subscription.username == username })
}

func (b *broker) disconnectSession(sessionHash string) {
	b.disconnect(func(subscription *subscriber) bool { return subscription.sessionHash == sessionHash })
}

func (b *broker) disconnect(matches func(*subscriber) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for subscription := range b.subscribers {
		if matches(subscription) {
			delete(b.subscribers, subscription)
			close(subscription.updates)
		}
	}
}
