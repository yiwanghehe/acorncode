package tool

import (
	"testing"
)

func TestRegistry_RegisterEditAndGet(t *testing.T) {
	r := NewRegistry()
	edit := r.RegisterEdit("/tmp")
	if edit == nil {
		t.Fatal("RegisterEdit 返 nil")
	}

	got, ok := r.Get("edit")
	if !ok {
		t.Fatal("Get('edit') 返 false")
	}
	if got != edit {
		t.Errorf("Get('edit') 返不同的指针: %T vs %T", got, edit)
	}
}

func TestRegistry_RegisterAllAndList(t *testing.T) {
	r := NewRegistry()
	r.RegisterRead("/tmp")
	r.RegisterEdit("/tmp")
	r.RegisterBash("/tmp")

	defs := r.Definitions()
	if len(defs) != 3 {
		t.Errorf("Definitions = %d, 期望 3", len(defs))
	}
	for _, def := range defs {
		t.Logf("tool: %s", def.ID)
	}
}
