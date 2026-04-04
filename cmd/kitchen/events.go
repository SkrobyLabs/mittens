package main

import (
	"sync"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func (k *Kitchen) SubscribeNotifications(buffer int) (<-chan pool.Notification, func()) {
	if k == nil {
		return nil, func() {}
	}
	if buffer <= 0 {
		buffer = 16
	}
	ch := make(chan pool.Notification, buffer)

	k.notifyMu.Lock()
	if k.notifySubs == nil {
		k.notifySubs = make(map[int]chan pool.Notification)
	}
	id := k.notifySeq
	k.notifySeq++
	k.notifySubs[id] = ch
	k.notifyMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			k.notifyMu.Lock()
			sub, ok := k.notifySubs[id]
			if ok {
				delete(k.notifySubs, id)
			}
			k.notifyMu.Unlock()
			if ok {
				close(sub)
			}
		})
	}
	return ch, cancel
}

func (k *Kitchen) sendNotify(n pool.Notification) {
	if k == nil {
		return
	}
	k.notifyMu.RLock()
	for _, ch := range k.notifySubs {
		select {
		case ch <- n:
		default:
		}
	}
	k.notifyMu.RUnlock()
}
