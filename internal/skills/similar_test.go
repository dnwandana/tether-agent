package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// writeRailSkill writes the skill name with description under dir.
func writeRailSkill(t *testing.T, dir, name, desc string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + strconv.Quote(desc) + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSimilarByNameParts(t *testing.T) {
	dir := t.TempDir()
	writeRailSkill(t, dir, "graphkb-lightrag", "LightRAG graph knowledge base for notes")
	existing, archived, err := SimilarSkills(dir, "lightrag-graphkb-notes", "Notes in a graph")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(existing, []string{"graphkb-lightrag"}) || len(archived) != 0 {
		t.Fatalf("%v %v", existing, archived)
	}
}

func TestSimilarByWords(t *testing.T) {
	dir := t.TempDir()
	writeRailSkill(t, dir, "vps-services", "Manage the services of the VPS: restart, logs, ports, docker")
	existing, _, err := SimilarSkills(dir, "server-ops", "Restart services and read docker logs on the VPS")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(existing, []string{"vps-services"}) {
		t.Fatalf("%v", existing)
	}
}

func TestNotSimilar(t *testing.T) {
	dir := t.TempDir()
	writeRailSkill(t, dir, "vps-services", "Manage the services of the VPS: restart, logs, ports, docker")
	writeRailSkill(t, dir, "graphkb-lightrag", "LightRAG graph knowledge base for notes")
	existing, archived, err := SimilarSkills(dir, "pdf-tools", "Merge and split PDF files")
	if err != nil {
		t.Fatal(err)
	}
	if len(existing) != 0 || len(archived) != 0 {
		t.Fatalf("%v %v", existing, archived)
	}
}

func TestSimilarArchived(t *testing.T) {
	dir := t.TempDir()
	writeRailSkill(t, filepath.Join(dir, ".archive"), "graphkb-lightrag-7", "x")
	// The folder name differs from the skill name on purpose.
	p := filepath.Join(dir, ".archive", "graphkb-lightrag-7", "SKILL.md")
	if err := os.WriteFile(p, []byte("---\nname: graphkb-lightrag\ndescription: x\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	existing, archived, err := SimilarSkills(dir, "graphkb-lightrag-v2", "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(existing) != 0 || !reflect.DeepEqual(archived, []string{"graphkb-lightrag"}) {
		t.Fatalf("%v %v", existing, archived)
	}
}

func TestSimilarSkipsItself(t *testing.T) {
	dir := t.TempDir()
	writeRailSkill(t, dir, "graphkb-lightrag", "LightRAG graph knowledge base for notes")
	existing, _, err := SimilarSkills(dir, "graphkb-lightrag", "LightRAG graph knowledge base for notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(existing) != 0 {
		t.Fatalf("%v", existing)
	}
}

func TestSimilarOrder(t *testing.T) {
	dir := t.TempDir()
	writeRailSkill(t, dir, "deploy-server-y", "nothing")
	writeRailSkill(t, dir, "zzz-one", "restart docker logs ports extra words")
	existing, _, err := SimilarSkills(dir, "deploy-server-tool", "restart docker logs ports")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(existing, []string{"zzz-one", "deploy-server-y"}) {
		t.Fatalf("%v", existing)
	}
}
