package access

import (
	"context"
	"testing"
	"time"
)

func TestList(t *testing.T) {
	ctx := context.Background()
	st := &MemoryStore{Users: map[int64]User{5: {ID: 5, Name: "Старый", AddedAt: time.Unix(10, 0)}}}

	l, err := Load(ctx, st, 1, []int64{5, 7})
	if err != nil {
		t.Fatal(err)
	}
	if st.Users[5].Name != "Старый" {
		t.Error("seed перезаписал существующего пользователя")
	}
	for id, want := range map[int64]bool{1: true, 5: true, 7: true, 9: false} {
		if l.Allowed(id) != want {
			t.Errorf("Allowed(%d) = %v", id, !want)
		}
	}
	if !l.FromEnv(7) || l.FromEnv(9) {
		t.Error("FromEnv")
	}
	if !l.IsAdmin(1) || l.IsAdmin(5) {
		t.Error("IsAdmin")
	}
	if ids := l.IDs(); len(ids) != 3 || ids[0] != 1 {
		t.Errorf("IDs: %v", ids)
	}

	if added, err := l.Add(ctx, User{ID: 9, Name: "Новый", AddedAt: time.Now()}); err != nil || !added {
		t.Fatalf("Add: %v %v", added, err)
	}
	if added, _ := l.Add(ctx, User{ID: 9}); added {
		t.Error("повторное добавление")
	}
	if !l.Allowed(9) || st.Users[9].Name != "Новый" {
		t.Error("новый пользователь не сохранён")
	}

	if err := l.RememberName(ctx, 7, "Семь"); err != nil || st.Users[7].Name != "Семь" {
		t.Errorf("RememberName: %v %+v", err, st.Users[7])
	}
	l.RememberName(ctx, 7, "Другое")
	if st.Users[7].Name != "Семь" {
		t.Error("имя перезаписано")
	}

	if removed, err := l.Remove(ctx, 5); err != nil || !removed || l.Allowed(5) {
		t.Fatalf("Remove: %v %v", removed, err)
	}
	if removed, _ := l.Remove(ctx, 5); removed {
		t.Error("повторное удаление")
	}
	if us := l.Users(); len(us) != 2 || us[0].ID != 7 || us[1].ID != 9 {
		t.Errorf("Users: %+v", us)
	}

	// Без администратора никто не админ.
	l, _ = Load(ctx, &MemoryStore{}, 0, nil)
	if l.IsAdmin(0) || l.Allowed(0) {
		t.Error("пустой админ")
	}
}
