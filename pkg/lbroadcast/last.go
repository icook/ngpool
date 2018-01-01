package lbroadcast

// This is a simple fork of github.com/dustin/go-broadcast that stores the last
// broadcasted item and sends it to new listeners when the register. Nearly
// identical code, but since it's not public method I couldn't easily inherit

import (
	"github.com/dustin/go-broadcast"
)

type broadcaster struct {
	input chan interface{}
	reg   chan chan<- interface{}
	unreg chan chan<- interface{}

	outputs map[chan<- interface{}]bool
}

func (b *broadcaster) run() {
	var lastSend interface{}
	for {
		select {
		case m := <-b.input:
			lastSend = m
			for ch := range b.outputs {
				ch <- m
			}
		case ch, ok := <-b.reg:
			if ok {
				b.outputs[ch] = true
				ch <- lastSend
			} else {
				return
			}
		case ch := <-b.unreg:
			delete(b.outputs, ch)
		}
	}
}

// NewBroadcaster creates a new broadcaster with the given input
// channel buffer length.
func NewLastBroadcaster(buflen int) broadcast.Broadcaster {
	b := &broadcaster{
		input:   make(chan interface{}, buflen),
		reg:     make(chan chan<- interface{}),
		unreg:   make(chan chan<- interface{}),
		outputs: make(map[chan<- interface{}]bool),
	}

	go b.run()

	return b
}

func (b *broadcaster) Register(newch chan<- interface{}) {
	b.reg <- newch
}

func (b *broadcaster) Unregister(newch chan<- interface{}) {
	b.unreg <- newch
}

func (b *broadcaster) Close() error {
	close(b.reg)
	return nil
}

func (b *broadcaster) Submit(m interface{}) {
	if b != nil {
		b.input <- m
	}
}
