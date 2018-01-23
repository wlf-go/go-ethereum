// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// Package event deals with subscriptions to real-time events.
package event

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
)

// TypeMuxEvent is a time-tagged notification pushed to subscribers.
//时间机制触发通知推送给订阅者
type TypeMuxEvent struct {
	Time time.Time
	Data interface{}
}

// A TypeMux dispatches events to registered receivers. Receivers can be
// registered to handle events of certain type. Any operation
// called after mux is stopped will return ErrMuxClosed.
//
// The zero value is ready to use.
//
// Deprecated: use Feed
//订阅注册中心
type TypeMux struct {
	mutex   sync.RWMutex
	subm    map[reflect.Type][]*TypeMuxSubscription
	stopped bool
}

// ErrMuxClosed is returned when Posting on a closed TypeMux.
var ErrMuxClosed = errors.New("event: mux closed")

// Subscribe creates a subscription for events of the given types. The
// subscription's channel is closed when it is unsubscribed
// or the mux is closed.
//开启订阅，并将此通道放入选择的不同类型通道注册中心
func (mux *TypeMux) Subscribe(types ...interface{}) *TypeMuxSubscription {
	//创建订阅通道
	sub := newsub(mux)
	//加锁
	mux.mutex.Lock()
	//最终执行解锁操作，类似java中finally
	defer mux.mutex.Unlock()
	if mux.stopped {
		// set the status to closed so that calling Unsubscribe after this
		// call will short circuit.
		//注册中心不可用，则要关闭此订阅并关闭写通道
		sub.closed = true
		close(sub.postC)
	} else {
		//当前注册中心中还没有订阅数据，则需要创建订阅信息存储结构
		if mux.subm == nil {
			mux.subm = make(map[reflect.Type][]*TypeMuxSubscription)
		}
		//将当前订阅放入不同类型的订阅数据中心
		for _, t := range types {
			//获取当前类型订阅数据
			rtyp := reflect.TypeOf(t)
			oldsubs := mux.subm[rtyp]
			if find(oldsubs, sub) != -1 {
				panic(fmt.Sprintf("event: duplicate type %s in Subscribe", rtyp))
			}
			//将当前订阅信息添加到此类型订阅数据中心
			subs := make([]*TypeMuxSubscription, len(oldsubs)+1)
			copy(subs, oldsubs)
			subs[len(oldsubs)] = sub
			mux.subm[rtyp] = subs
		}
	}
	return sub
}

// Post sends an event to all receivers registered for the given type.
// It returns ErrMuxClosed if the mux has been stopped.
//推送订阅信息给所有此类型消息的接受者
func (mux *TypeMux) Post(ev interface{}) error {
	//创建消息事件
	event := &TypeMuxEvent{
		Time: time.Now(),
		Data: ev,
	}
	//获取类型
	rtyp := reflect.TypeOf(ev)
	//加锁
	mux.mutex.RLock()
	if mux.stopped {
		mux.mutex.RUnlock()
		return ErrMuxClosed
	}
	//读取订阅通道数据
	subs := mux.subm[rtyp]
	//解锁
	mux.mutex.RUnlock()
	//将消息事件推送给消息订阅者
	for _, sub := range subs {
		sub.deliver(event)
	}
	return nil
}

// Stop closes a mux. The mux can no longer be used.
// Future Post calls will fail with ErrMuxClosed.
// Stop blocks until all current deliveries have finished.
//关闭注册中心
func (mux *TypeMux) Stop() {
	mux.mutex.Lock()
	for _, subs := range mux.subm {
		for _, sub := range subs {
			sub.closewait()
		}
	}
	mux.subm = nil
	mux.stopped = true
	mux.mutex.Unlock()
}

//从注册中心中删除订阅信息（各类型）
func (mux *TypeMux) del(s *TypeMuxSubscription) {
	mux.mutex.Lock()
	for typ, subs := range mux.subm {
		if pos := find(subs, s); pos >= 0 {
			if len(subs) == 1 {
				delete(mux.subm, typ)
			} else {
				mux.subm[typ] = posdelete(subs, pos)
			}
		}
	}
	s.mux.mutex.Unlock()
}

func find(slice []*TypeMuxSubscription, item *TypeMuxSubscription) int {
	for i, v := range slice {
		if v == item {
			return i
		}
	}
	return -1
}

func posdelete(slice []*TypeMuxSubscription, pos int) []*TypeMuxSubscription {
	news := make([]*TypeMuxSubscription, len(slice)-1)
	copy(news[:pos], slice[:pos])
	copy(news[pos:], slice[pos+1:])
	return news
}

// TypeMuxSubscription is a subscription established through TypeMux.
//订阅信息存储模型
type TypeMuxSubscription struct {
	mux     *TypeMux
	created time.Time
	closeMu sync.Mutex
	closing chan struct{}
	closed  bool

	// these two are the same channel. they are stored separately so
	// postC can be set to nil without affecting the return value of
	// Chan.
	//读写锁
	postMu sync.RWMutex
	//只读通道
	readC <-chan *TypeMuxEvent
	//写通道
	postC chan<- *TypeMuxEvent
}

//创建订阅信息
func newsub(mux *TypeMux) *TypeMuxSubscription {
	c := make(chan *TypeMuxEvent)
	return &TypeMuxSubscription{
		mux:     mux,
		created: time.Now(),
		readC:   c,
		postC:   c,
		closing: make(chan struct{}),
	}
}

func (s *TypeMuxSubscription) Chan() <-chan *TypeMuxEvent {
	return s.readC
}

func (s *TypeMuxSubscription) Unsubscribe() {
	s.mux.del(s)
	s.closewait()
}

func (s *TypeMuxSubscription) closewait() {
	//关闭此订阅信息
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return
	}
	close(s.closing)
	s.closed = true

	//关闭此订阅信息的写通道
	s.postMu.Lock()
	close(s.postC)
	s.postC = nil
	s.postMu.Unlock()
}

func (s *TypeMuxSubscription) deliver(event *TypeMuxEvent) {
	// Short circuit delivery if stale event
	//通道创建在消息创建之前，无效
	if s.created.After(event.Time) {
		return
	}
	// Otherwise deliver the event
	//锁定此订阅通道
	s.postMu.RLock()
	//方法执行完，最终解锁
	defer s.postMu.RUnlock()

	//监听通道io操作，将消息放入写通道，如果写通道已满，则将此订阅通道置为待关闭状态
	select {
	case s.postC <- event:
	case <-s.closing:
	}
}
