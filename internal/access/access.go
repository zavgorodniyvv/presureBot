// Package access — кому можно пользоваться ботом: администратор и список
// разрешённых пользователей, который хранится в базе и меняется командами админа.
// Пакет не зависит от Telegram и конкретной базы — его можно переносить в другие боты.
package access

import (
	"context"
	"sort"
	"sync"
	"time"
)

type User struct {
	ID      int64
	Name    string // имя из Telegram, может быть пустым
	AddedAt time.Time
	AddedBy int64 // 0 — перенесён из ALLOWED_USER_IDS
}

// Store — постоянное хранилище списка.
type Store interface {
	AllowedUsers(ctx context.Context) ([]User, error)
	// AddAllowedUser добавляет пользователя; если он уже есть, ничего не меняет (added=false).
	AddAllowedUser(ctx context.Context, u User) (added bool, err error)
	RemoveAllowedUser(ctx context.Context, id int64) (removed bool, err error)
	SetAllowedUserName(ctx context.Context, id int64, name string) error
}

// List — список в памяти поверх Store. Безопасен для конкурентного использования.
type List struct {
	store Store
	admin int64          // 0 — администратора нет
	seed  map[int64]bool // ID из ALLOWED_USER_IDS — возвращаются в список при каждом старте

	mu    sync.RWMutex
	users map[int64]User
}

// Load читает список из базы. Пользователи из seed (ALLOWED_USER_IDS), которых
// там ещё нет, добавляются — так доступ, заданный в env, не пропадает.
func Load(ctx context.Context, store Store, admin int64, seed []int64) (*List, error) {
	for _, id := range seed {
		if _, err := store.AddAllowedUser(ctx, User{ID: id, AddedAt: time.Now()}); err != nil {
			return nil, err
		}
	}
	list, err := store.AllowedUsers(ctx)
	if err != nil {
		return nil, err
	}
	l := &List{store: store, admin: admin, seed: make(map[int64]bool, len(seed)), users: make(map[int64]User, len(list))}
	for _, id := range seed {
		l.seed[id] = true
	}
	for _, u := range list {
		l.users[u.ID] = u
	}
	return l, nil
}

func (l *List) IsAdmin(id int64) bool { return l.admin != 0 && id == l.admin }

func (l *List) Admin() int64 { return l.admin }

// FromEnv — ID задан в ALLOWED_USER_IDS: удаление из базы продержится только до перезапуска.
func (l *List) FromEnv(id int64) bool { return l.seed[id] }

func (l *List) Allowed(id int64) bool {
	if l.IsAdmin(id) {
		return true
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.users[id]
	return ok
}

// IDs — все, кто может пользоваться ботом, включая администратора.
func (l *List) IDs() []int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	ids := make([]int64, 0, len(l.users)+1)
	for id := range l.users {
		ids = append(ids, id)
	}
	if _, ok := l.users[l.admin]; l.admin != 0 && !ok {
		ids = append(ids, l.admin)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Users — список без администратора, по порядку добавления.
func (l *List) Users() []User {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]User, 0, len(l.users))
	for _, u := range l.users {
		if u.ID != l.admin {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].AddedAt.Equal(out[j].AddedAt) {
			return out[i].AddedAt.Before(out[j].AddedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (l *List) Add(ctx context.Context, u User) (bool, error) {
	added, err := l.store.AddAllowedUser(ctx, u)
	if err != nil || !added {
		return false, err
	}
	l.mu.Lock()
	l.users[u.ID] = u
	l.mu.Unlock()
	return true, nil
}

func (l *List) Remove(ctx context.Context, id int64) (bool, error) {
	removed, err := l.store.RemoveAllowedUser(ctx, id)
	if err != nil {
		return false, err
	}
	l.mu.Lock()
	delete(l.users, id)
	l.mu.Unlock()
	return removed, nil
}

// RememberName сохраняет имя, если оно ещё неизвестно (добавлен по ID или из env).
func (l *List) RememberName(ctx context.Context, id int64, name string) error {
	l.mu.RLock()
	u, ok := l.users[id]
	l.mu.RUnlock()
	if !ok || u.Name != "" || name == "" {
		return nil
	}
	if err := l.store.SetAllowedUserName(ctx, id, name); err != nil {
		return err
	}
	l.mu.Lock()
	if u, ok := l.users[id]; ok {
		u.Name = name
		l.users[id] = u
	}
	l.mu.Unlock()
	return nil
}

// MemoryStore — Store в памяти, для тестов.
type MemoryStore struct {
	mu    sync.Mutex
	Users map[int64]User
}

func (s *MemoryStore) AllowedUsers(context.Context) ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]User, 0, len(s.Users))
	for _, u := range s.Users {
		out = append(out, u)
	}
	return out, nil
}

func (s *MemoryStore) AddAllowedUser(_ context.Context, u User) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Users[u.ID]; ok {
		return false, nil
	}
	if s.Users == nil {
		s.Users = map[int64]User{}
	}
	s.Users[u.ID] = u
	return true, nil
}

func (s *MemoryStore) RemoveAllowedUser(_ context.Context, id int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.Users[id]
	delete(s.Users, id)
	return ok, nil
}

func (s *MemoryStore) SetAllowedUserName(_ context.Context, id int64, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.Users[id]; ok {
		u.Name = name
		s.Users[id] = u
	}
	return nil
}
